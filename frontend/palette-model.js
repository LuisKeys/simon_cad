// The command palette's rules, apart from the DOM: which entries a query shows and how a tool's
// arguments are prefilled from the current selection.
import { featureOf } from './actions.js';

export const MAX_ENTRIES = 40;

/** The entries matching `query` (title, id or description, case-insensitive), at most MAX_ENTRIES,
 *  followed by an "ask the agent" entry when something was typed. */
export function filterEntries(all, query, max = MAX_ENTRIES) {
  const t = query.trim().toLowerCase();
  const hits = all.filter((e) => !t || e.title.toLowerCase().includes(t) || e.id.toLowerCase().includes(t) || e.description.toLowerCase().includes(t));
  const items = hits.slice(0, max);
  if (t) items.push({ kind: 'ask', id: 'ask', title: `Ask the agent: \u201c${query.trim()}\u201d`, description: 'Natural language through the agent', template: query.trim() });
  return items;
}

/** A tool that takes a face selector gets the selected face; fillet/chamfer the selected edges and
 *  their feature; a single selected object fills the feature the tool acts on (`target` / `source`
 *  left empty in the template). Mutates and returns args. */
export function prefillSelection(args, entry, selectors) {
  const sels = selectors ?? [];
  const props = entry.schema?.properties ?? {};
  const face = sels.find((x) => x.startsWith('face('));
  if (face && props.face !== undefined && 'face' in args) args.face = face;
  const edges = sels.filter((x) => x.startsWith('edge('));
  if (edges.length && props.edges !== undefined) {
    args.edges = edges;
    args.target = featureOf(edges[0]);
  }
  const feats = [...new Set(sels.map(featureOf).filter(Boolean))];
  if (feats.length === 1) for (const k of ['target', 'source']) if (args[k] === '') args[k] = feats[0];
  return args;
}
