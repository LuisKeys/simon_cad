// Help microsite behaviour: table of contents, scroll-spy, search, copy-to-clipboard prompts,
// and the tool reference (rendered from the table below, mirroring docs/tools.md).

const TOOLS = [
  // [name, kind, summary, args ("*" = required), example JSON]
  ['create_box', 'edit', 'Rectangular block. dx/dy/dz along X/Y/Z, minimum corner at (x,y,z).', 'dx* dy* dz* x y z', '{ "dx": 100, "dy": 60, "dz": 4 }'],
  ['create_cylinder', 'edit', 'Cylinder (shaft, boss, standoff, pin) along axis x|y|z (default z).', 'radius* height* axis x y z', '{ "radius": 6, "height": 20, "x": 10, "y": 10 }'],
  ['create_sphere', 'edit', 'Sphere centred at (x,y,z).', 'radius* x y z', '{ "radius": 15 }'],
  ['create_cone', 'edit', 'Cone or frustum; top_radius 0 = a point.', 'radius* height* top_radius axis x y z', '{ "radius": 20, "height": 30, "top_radius": 8 }'],
  ['create_torus', 'edit', 'Ring / O-ring seat. minor_radius must be smaller than major_radius.', 'major_radius* minor_radius* axis x y z', '{ "major_radius": 25, "minor_radius": 3 }'],
  ['create_sketch', 'edit', 'Planar 2D sketch (line, arc, circle, rectangle, polygon, slot) on XY/YZ/XZ or on a planar face. No solid yet.', 'entities* plane offset face regions', '{ "plane": "XY", "entities": [ { "type": "rectangle", "x": 0, "y": 0, "w": 40, "h": 20 }, { "type": "circle", "cx": 20, "cy": 10, "r": 4 } ] }'],
  ['extrude_sketch', 'edit', 'Extrude regions of an existing sketch: add material or cut. through / symmetric / reverse supported.', 'sketch* depth mode target region through symmetric reverse', '{ "sketch": "sketch_1", "depth": 5, "mode": "add" }'],
  ['extrude_profile', 'edit', 'One-shot: sketch a rectangle / circle / slot / polygon and extrude it (add or cut).', 'profile depth mode target plane offset through symmetric x y w h r …', '{ "profile": "circle", "x": 30, "y": 30, "r": 5, "mode": "cut", "target": "plate", "through": true }'],
  ['add_hole', 'edit', 'Drill holes (simple, counterbore, countersink) into a face. Omit depth for a through hole.', 'diameter* target face positions depth kind cb_diameter cb_depth cs_diameter cs_angle_deg', '{ "target": "plate", "diameter": 3.4, "positions": [[10,10],[90,10],[10,50],[90,50]] }'],
  ['fillet', 'edit', 'Round edges. All edges of the body, or only the selected edge(…) selectors.', 'target* radius* edges', '{ "target": "plate", "radius": 3 }'],
  ['chamfer', 'edit', 'Chamfer edges by a distance (mm).', 'target* distance* edges', '{ "target": "plate", "distance": 1 }'],
  ['boolean', 'edit', 'union / subtract / intersect two features. Both inputs are consumed.', 'target* tool* op', '{ "target": "base", "tool": "pocket_tool", "op": "subtract" }'],
  ['linear_pattern', 'edit', 'Repeat a hole or cut feature count times, offset (dx,dy,dz) per copy.', 'source* count* dx dy dz', '{ "source": "hole_1", "count": 5, "dx": 15 }'],
  ['circular_pattern', 'edit', 'Repeat a hole or cut around an axis through (cx,cy,cz). Default 360°.', 'source* count* axis cx cy cz total_angle_deg', '{ "source": "hole_1", "count": 6, "axis": "z", "cx": 50, "cy": 30 }'],
  ['mirror', 'edit', 'Mirror across XY / XZ / YZ (optionally shifted by offset) and merge.', 'target* plane offset', '{ "target": "arm", "plane": "YZ" }'],
  ['transform', 'edit', 'Rotate (angle_deg about axis through origin point), then translate by (dx,dy,dz).', 'target* angle_deg ax ay az ox oy oz dx dy dz', '{ "target": "arm", "angle_deg": 90, "ax": 0, "ay": 0, "az": 1, "dx": 50 }'],
  ['scale', 'edit', 'Scale a body: uniform factor or per-axis sx/sy/sz, about its centre, its base (stays on the bed), the origin or a point.', 'target* factor sx sy sz about ox oy oz', '{ "target": "bracket", "factor": 1.5, "about": "base" }'],
  ['duplicate', 'edit', 'Linked copies of bodies, offset by (dx,dy,dz). The source stays; the copy follows its parameter edits and keeps its appearance and group.', 'features dx dy dz', '{ "features": ["standoff"], "dx": 20 }'],
  ['align', 'edit', 'Align bodies’ min / centre / max along axes, to the first body, the selection bounds, the origin or a reference body.', 'axes* features mode to reference', '{ "features": ["base", "boss"], "axes": ["x", "y"], "mode": "center" }'],
  ['face_extrude', 'edit', 'Push/pull a planar face along its normal: + adds material, − cuts.', 'face* distance*', '{ "face": "face(feature=plate,side=+Z)", "distance": 5 }'],
  ['face_inset', 'edit', 'Shrink a planar face’s outline by inset; with depth make a pad (+) or pocket (−), without depth just split the face.', 'face* inset* depth', '{ "face": "face(feature=lid,side=+Z)", "inset": 2, "depth": -3 }'],
  ['face_offset', 'edit', 'Move any face along its normal; neighbours follow. On a hole wall, − enlarges the hole.', 'face* distance*', '{ "face": "face(feature=plate,index=7)", "distance": -0.2 }'],
  ['group', 'edit', 'Put features in a named design-tree group (organization only; also renames a group).', 'name* features', '{ "features": ["base", "boss"], "name": "Chassis" }'],
  ['ungroup', 'edit', 'Remove features from their group, or dissolve a whole group.', 'features group', '{ "group": "Chassis" }'],
  ['create_component', 'edit', 'Move the selected features into a new reusable project (a component) and replace them in place with one instance. The selection must be self-contained.', 'name* description tags features id intent', '{ "name": "Wheel", "features": ["wheel"], "tags": ["chassis"] }'],
  ['insert_component', 'edit', 'Place an instance of a library component: a reference, not a copy. Once per instance.', 'component* name id dx dy dz angle_deg ax ay az ox oy oz sx sy sz intent', '{ "component": "Wheel", "name": "Wheel FL", "dx": 120, "dy": -80 }'],
  ['sync_component', 'edit', 'Re-pin instances to the current state of their component after it was edited. No arguments syncs every out-of-date instance.', 'instances', '{ }'],
  ['detach_component', 'edit', 'Replace an instance with its own local geometry; the component no longer drives it.', 'instance* intent', '{ "instance": "wheel_fl" }'],
  ['modify_parameter', 'edit', 'Change one parameter of an existing feature (e.g. plate dz 4 → 5).', 'feature* parameter* value* intent', '{ "feature": "plate", "parameter": "dz", "value": 5 }'],
  ['set_expression', 'edit', 'Drive a parameter by an expression over other features. Omit expression to clear.', 'feature* parameter* expression', '{ "feature": "boss", "parameter": "height", "expression": "plate.dz * 3" }'],
  ['rename_feature', 'edit', 'Change a feature’s display name (the id stays).', 'feature* name*', '{ "feature": "box_1", "name": "Base plate" }'],
  ['suppress_feature', 'edit', 'Suppress / unsuppress a feature (it passes its target through unchanged).', 'feature* suppressed*', '{ "feature": "fillet_1", "suppressed": true }'],
  ['remove_feature', 'edit', 'Delete a feature; fails if others depend on it.', 'feature*', '{ "feature": "hole_2" }'],
  ['delete_objects', 'edit', 'Delete the selection or listed features as ONE undoable ChangeSet. cascade / resolve_owner for dependants and sub-shape selections.', 'features cascade resolve_owner', '{ "features": ["boss"], "cascade": false }'],
  ['restore_checkpoint', 'edit', 'Return to an earlier semantic checkpoint (id, number, "previous" or part of its label). Always previewed.', 'checkpoint*', '{ "checkpoint": "previous" }'],
  ['set_color', 'edit', 'Display colour (name, #hex or rgb()). Presentation only.', 'color* features', '{ "color": "#c0392b", "features": ["bracket"] }'],
  ['set_opacity', 'edit', 'Display opacity 0–1. Presentation only.', 'opacity* features', '{ "opacity": 0.5 }'],
  ['set_visibility', 'edit', 'Show / hide bodies. Hidden bodies still exist and are exported.', 'visible* features', '{ "visible": false, "features": ["servo_ref"] }'],
  ['reset_appearance', 'edit', 'Reset colour, opacity and visibility.', 'features', '{ }'],
  ['set_material', 'edit', 'Assign a library material. Presentation only.', 'material_id* features keep_color', '{ "material_id": "brushed_aluminum", "features": ["bracket"] }'],
  ['set_material_scale', 'edit', 'Texture scale multiplier (1 = natural).', 'scale* features', '{ "scale": 2 }'],
  ['remove_material', 'edit', 'Remove the material (colour / opacity overrides stay).', 'features', '{ }'],
  ['create_from_image', 'edit', 'Turn a ready image interpretation into sketch + extrude/cut features. Always left proposed.', 'interpretation_id extrusion_depth plane offset face mode', '{ "extrusion_depth": 6 }'],
  ['create_sketch_from_image', 'edit', 'Only the planar sketch from an interpretation.', 'interpretation_id plane offset face', '{ "plane": "XY" }'],
  ['extrude_detected_profile', 'edit', 'Extrude / cut the profiles of an interpretation whose sketch already exists.', 'interpretation_id extrusion_depth …', '{ "extrusion_depth": 6 }'],
  ['list_components', 'query', 'The reusable components in the library.', '', '{ }'],
  ['search_components', 'query', 'Search the library by name, description or tag.', 'query*', '{ "query": "wheel" }'],
  ['open_component', 'query', 'One component’s details and project path. Does not switch projects.', 'component*', '{ "component": "Wheel" }'],
  ['get_model_summary', 'query', 'Every feature, parameters, bounding boxes, volumes and recent checkpoints. The agent calls it before editing an unfamiliar model.', '', '{ }'],
  ['measure_distance', 'query', 'Gap between two faces/bodies (perpendicular for parallel faces, else centroid distance).', 'from* to*', '{ "from": "face(feature=plate,side=+Z)", "to": "body(feature=boss)" }'],
  ['check_delete', 'query', 'Dry run of delete_objects: owners and dependants. Changes nothing.', 'features cascade resolve_owner', '{ }'],
  ['export_step', 'query', 'Write a STEP file.', 'path*', '{ "path": "out/bracket.step" }'],
  ['export_stl', 'query', 'Write a binary STL for 3D printing.', 'path* deflection_mm', '{ "path": "out/bracket.stl", "deflection_mm": 0.05 }'],
  ['get_appearance', 'query', 'Read colour / opacity / visibility.', 'features', '{ }'],
  ['list_materials', 'query', 'List library materials, optionally for one category.', 'category', '{ "category": "Metals" }'],
  ['search_materials', 'query', 'Free-text material search; returns ≤ 5 ranked candidates and an ambiguous flag.', 'query*', '{ "query": "black plastic" }'],
  ['get_material', 'query', 'One material’s properties.', 'material_id*', '{ "material_id": "titanium" }'],
  ['set_mesh_detail', 'query', 'Display tessellation: coarse | normal | fine. Geometry and exports unchanged.', 'level*', '{ "level": "coarse" }'],
  ['analyze_image', 'query', 'Agent records what it sees in an image as a structured proposal (no model change).', 'image_id* image_size* entities* profiles* confidence* dimensions …', '(filled in by the agent)'],
  ['get_sketch_proposal', 'query', 'Read an interpretation and its validation report.', 'interpretation_id', '{ }'],
  ['update_sketch_proposal', 'query', 'Correct the proposal (not the model) with set_/remove_ edits.', 'edits* interpretation_id', '{ "edits": [ … ] }'],
  ['confirm_detected_dimension', 'query', 'Confirm or correct a detected dimension in mm.', 'dimension* value interpretation_id', '{ "dimension": "width", "value": 80 }'],
  ['set_sketch_scale', 'query', 'Set the real scale of a drawing (mm_per_px, or pixels = mm).', 'mm_per_px | pixels+mm | clear', '{ "pixels": 400, "mm": 80 }'],
];

const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));

function renderTools() {
  const host = document.getElementById('tool-list');
  if (!host) return;
  host.innerHTML = TOOLS.map(([name, kind, desc, args, ex]) => {
    const a = args ? args.split(' ').map((x) => (x.endsWith('*') ? `<code class="req">${esc(x.slice(0, -1))}</code>` : `<code>${esc(x)}</code>`)).join(' ') : '<span class="muted">no arguments</span>';
    return `<details class="tool" data-search="${esc(name + ' ' + desc)}"><summary><span class="nm">${name}</span><span class="badge ${kind}">${kind === 'edit' ? 'ChangeSet' : 'query'}</span><span class="ds">${esc(desc)}</span></summary>
      <div class="body"><p><b>Arguments</b> (<span class="req">orange</span> = required): ${a}</p><div class="prompt json" data-copy>${esc(ex)}</div></div></details>`;
  }).join('');
}

function renderMaterials() {
  const host = document.getElementById('materials');
  if (!host) return;
  const M = [
    ['brushed_aluminum', 'Brushed Aluminum', '#BFC2C4'], ['polished_aluminum', 'Polished Aluminum', '#D9DBDD'], ['anodized_black_aluminum', 'Black Anodized Aluminum', '#2a2a2c'],
    ['anodized_blue_aluminum', 'Blue Anodized Aluminum', '#2f5da8'], ['stainless_steel', 'Stainless Steel', '#c4c7c9'], ['polished_steel', 'Polished Steel', '#d0d2d4'],
    ['raw_steel', 'Raw Steel', '#7d8083'], ['black_steel', 'Black Steel', '#2b2d2f'], ['titanium', 'Titanium', '#8f949a'], ['brass', 'Brass', '#c8a24b'], ['copper', 'Copper', '#b87333'],
    ['black_abs', 'Black ABS', '#1c1c1c'], ['white_abs', 'White ABS', '#ececec'], ['black_pla', 'Black PLA', '#222'], ['white_pla', 'White PLA', '#f0f0f0'], ['gray_pla', 'Gray PLA', '#8a8a8a'],
    ['petg', 'PETG', '#a9c8d8'], ['petg_black', 'Black PETG', '#202224'], ['nylon_white', 'White Nylon', '#e8e4d8'], ['transparent_plastic', 'Transparent Plastic', '#cfe4ee'],
    ['black_rubber', 'Black Rubber', '#171717'], ['gray_rubber', 'Gray Rubber', '#6a6a6a'], ['carbon_fiber', 'Carbon Fiber', '#1a1a1a'],
    ['clear_glass', 'Clear Glass', '#d6ecf2'], ['tinted_glass', 'Tinted Glass', '#5f7a80'], ['green_pcb', 'Green PCB', '#1f7a3a'], ['black_pcb', 'Black PCB', '#161616'],
    ['oak_wood', 'Oak Wood', '#b58b52'], ['plywood', 'Plywood', '#d2b27a'], ['generic_gray', 'Generic Gray', '#9a9a9a'], ['generic_white', 'Generic White', '#f2f2f2'],
  ];
  host.innerHTML = M.map(([id, n, c]) => `<div class="sw"><i style="background:${c}"></i><span>${esc(n)}<small>${id}</small></span></div>`).join('');
}

function decoratePrompts() {
  document.querySelectorAll('.prompt').forEach((el) => {
    if (el.querySelector('button')) return;
    const clone = el.cloneNode(true);
    clone.querySelectorAll('.muted').forEach((n) => n.remove());
    const text = clone.textContent;
    const b = document.createElement('button');
    b.type = 'button'; b.textContent = 'Copy';
    b.addEventListener('click', async () => {
      try { await navigator.clipboard.writeText(text.trim()); b.textContent = 'Copied ✓'; } catch { b.textContent = 'Press Ctrl+C'; }
      setTimeout(() => { b.textContent = 'Copy'; }, 1500);
    });
    el.appendChild(b);
  });
}

function buildToc() {
  const toc = document.getElementById('toc');
  let html = '';
  document.querySelectorAll('main h2[id]').forEach((h) => {
    const g = h.dataset.group;
    if (g && g !== buildToc.last) { html += `<h4>${esc(g)}</h4>`; buildToc.last = g; }
    html += `<a href="#${h.id}">${esc(h.textContent.trim())}</a>`;
  });
  toc.innerHTML = html;
  toc.addEventListener('click', (e) => { if (e.target.tagName === 'A') toc.classList.remove('open'); });
  const links = [...toc.querySelectorAll('a')];
  const spy = new IntersectionObserver((entries) => {
    entries.forEach((en) => {
      if (!en.isIntersecting) return;
      links.forEach((l) => l.classList.toggle('on', l.getAttribute('href') === `#${en.target.id}`));
    });
  }, { rootMargin: '-72px 0px -70% 0px' });
  document.querySelectorAll('main h2[id]').forEach((h) => spy.observe(h));
}

function setupSearch() {
  const input = document.getElementById('q');
  input.addEventListener('input', () => {
    const q = input.value.trim().toLowerCase();
    document.querySelectorAll('main .tool').forEach((t) => t.classList.toggle('hit-hidden', !!q && !(t.dataset.search || '').toLowerCase().includes(q)));
    document.querySelectorAll('main > section').forEach((s) => {
      const tools = s.querySelectorAll('.tool');
      const hit = tools.length ? s.querySelector('.tool:not(.hit-hidden)') || s.querySelector('h2').textContent.toLowerCase().includes(q) : s.textContent.toLowerCase().includes(q);
      s.classList.toggle('hit-hidden', !!q && !hit);
    });
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === '/' && document.activeElement !== input) { e.preventDefault(); input.focus(); }
    if (e.key === 'Escape') { input.value = ''; input.dispatchEvent(new Event('input')); input.blur(); }
  });
}

renderTools();
renderMaterials();
decoratePrompts();
buildToc();
setupSearch();
document.getElementById('menu-btn').addEventListener('click', () => document.getElementById('toc').classList.toggle('open'));
