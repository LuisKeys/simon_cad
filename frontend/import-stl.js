// Importing an STL: the toolbar button and dropping a file on the viewport. The server validates
// the mesh before keeping it (one closed, watertight, consistently oriented solid); a refused
// file is explained in a toast and the model is not touched.

export const UNIT_CHOICES = [
  { label: 'mm', detail: 'millimetres (most CAD and slicers)', value: 'mm' },
  { label: 'cm', detail: 'centimetres', value: 'cm' },
  { label: 'm', detail: 'metres', value: 'm' },
  { label: 'in', detail: 'inches', value: 'in' },
  { label: 'ft', detail: 'feet', value: 'ft' },
];

/** The .stl files of a FileList / array, in order. */
export function stlFiles(files) {
  return [...(files ?? [])].filter((f) => /\.stl$/i.test(f.name ?? ''));
}

/** True when a drag carries files (not a material or text). */
export function dragHasFiles(e) {
  return [...(e.dataTransfer?.types ?? [])].includes('Files');
}

/**
 * Uploads one file. A file refused only because of holes is not a dead end: the user is asked
 * whether to close them (adding flat patches, which invents surface) and, if so, it is sent again.
 * Declining rethrows the original refusal.
 */
export async function importWithRepair({ file, units, uploadMesh, confirm }) {
  try {
    return await uploadMesh(file, { units });
  } catch (e) {
    if (e.code !== 'mesh_has_holes') throw e;
    const yes = await confirm({
      title: 'Close the holes?',
      body: `${e.message}\n\nThe holes can be closed by adding flat patches. That invents surface where the file has none, so check the result. Close them and import?`,
      ok: 'Close holes and import', okClass: 'primary' });
    if (!yes) throw e;
    return uploadMesh(file, { units, fillHoles: true });
  }
}

/** Wires the Import button, its hidden input and viewport drops. */
export function initImport({ button, input, viewport, ask, confirm, toast, uploadMesh, refresh, busy }) {
  let importing = false;
  async function importFiles(files) {
    const list = stlFiles(files);
    if (!list.length || importing) return;
    const r = await ask({ title: 'STL units (the file does not say)', value: 'mm', choices: UNIT_CHOICES, ok: 'Import' });
    if (!r?.value) return;
    importing = true;
    button.disabled = true;
    // A question asked while the dialog is up (close the holes?) hides it, then brings it back.
    const askWhileBusy = async (q) => { busy.hide(); try { return await confirm(q); } finally { busy.show(); } };
    try {
      for (const file of list) {
        busy.show(`Importing ${file.name}`, 'Uploading the file');
        try {
          const o = await importWithRepair({ file, units: r.value, uploadMesh, confirm: askWhileBusy });
          busy.close();
          toast(o.ok ? `Imported ${file.name}` : o.error, o.ok ? 'ok' : 'bad', o.ok ? 4500 : 12000);
        } catch (e) { busy.close(); toast(e.message, 'bad', 15000); }
        refresh();
      }
    } finally { busy.close(); importing = false; button.disabled = false; }
  }
  button.addEventListener('click', () => input.click());
  input.addEventListener('change', () => { const f = [...input.files]; input.value = ''; importFiles(f); });
  viewport.addEventListener('dragover', (e) => { if (dragHasFiles(e)) { e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; } });
  viewport.addEventListener('drop', (e) => { if (dragHasFiles(e)) { e.preventDefault(); importFiles(e.dataTransfer.files); } });
  return { importFiles };
}
