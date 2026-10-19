// Help microsite behaviour: table of contents, scroll-spy, search, copy-to-clipboard prompts,
// and the tool reference (rendered from the table below, mirroring docs/tools.md).

const TOOLS = [
  // [name, kind, summary, args ("*" = required), example JSON]
  ['create_box', 'edit', 'Rectangular block. dx/dy/dz along X/Y/Z, minimum corner at (x,y,z).', 'dx* dy* dz* x y z', '{ "dx": 100, "dy": 60, "dz": 4 }'],
  ['create_cylinder', 'edit', 'Cylinder (shaft, boss, standoff, pin) along axis x|y|z (default z).', 'radius* height* axis x y z', '{ "radius": 6, "height": 20, "x": 10, "y": 10 }'],
  ['create_sphere', 'edit', 'Sphere centred at (x,y,z).', 'radius* x y z', '{ "radius": 15 }'],
  ['create_cone', 'edit', 'Cone or frustum; top_radius 0 = a point.', 'radius* height* top_radius axis x y z', '{ "radius": 20, "height": 30, "top_radius": 8 }'],
  ['create_torus', 'edit', 'Ring / O-ring seat. minor_radius must be smaller than major_radius.', 'major_radius* minor_radius* axis x y z', '{ "major_radius": 25, "minor_radius": 3 }'],
  ['import_stl', 'edit', 'Import an STL file as a solid body, only after checking it is one closed, watertight, consistently oriented, non-self-intersecting solid; a refused file says what is wrong and where; fill_holes closes the holes of an otherwise sound mesh. STL has no units: pass units.', 'path mesh filename units placement fill_holes decimate_to', '{ "path": "out/part.stl", "units": "mm", "placement": "base" }'],
  ['decimate_mesh', 'edit', 'Really reduce the triangles (faces) of an imported STL body, keeping its shape: flat areas and sharp corners are kept and the result is validated like an import. Unlike set_mesh_detail, which only changes how finely the viewport draws it, this changes the model; undo restores the full mesh.', 'feature* target_triangles ratio', '{ "feature": "bunny", "target_triangles": 10000 }'],
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
  ['set_object_dimension', 'edit', 'Set one dimension (width / depth / height = local X / Y / Z) of an object in mm. Only that one changes unless Lock Proportions is on. A non-parametric scale; undoable.', 'axis* value* object_id anchor proportional', '{ "object_id": "plate", "axis": "width", "value": 150 }'],
  ['move_object', 'edit', 'Move one object in world coordinates: relative dx/dy/dz or absolute x/y/z of its origin, in `unit` (default mm); snap applies the project snapping. One undoable change; a component instance\'s placement is updated.', 'object_id dx dy dz x y z unit snap', '{ "object_id": "arm", "dx": 20 }'],
  ['rotate_object', 'edit', 'Rotate one object about a world axis (default: the grid plane normal) through its origin, its center or a point; angle in angle_unit (default deg); snap uses the rotation increment. One undoable change.', 'object_id angle* angle_unit axis axis_vector pivot point unit snap', '{ "object_id": "arm", "angle": 45, "axis": "z", "pivot": "center" }'],
  ['resize_object', 'edit', 'Resize an object to a width / depth / height (mm); omitted dimensions keep their size. Anchor: center or object_origin.', 'object_id width depth height anchor', '{ "object_id": "plate", "width": 150, "height": 40 }'],
  ['set_object_proportional_scaling', 'edit', 'Lock Proportions on/off for an object: one dimension edit then scales all three. Editing preference only.', 'enabled* object_id', '{ "object_id": "plate", "enabled": true }'],
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
  ['batch', 'edit', 'Run several model-editing calls as ONE change: one undo step, all or nothing (used by the UI for operations on many objects; not offered to the agent).', 'calls* intent', '{ "calls": [{ "tool": "suppress_feature", "args": { "feature": "a", "suppressed": true } }, { "tool": "suppress_feature", "args": { "feature": "b", "suppressed": true } }] }'],
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
  ['get_object_transform', 'query', 'Where an object is: origin, box center, orientation, whether it can be moved now and how a move is recorded.', 'object_id', '{ "object_id": "arm" }'],
  ['get_object_dimensions', 'query', 'Width, depth and height of an object along its local axes, with its frame, box, center, origin and Lock Proportions.', 'object_id', '{ "object_id": "plate" }'],
  ['check_delete', 'query', 'Dry run of delete_objects: owners and dependants. Changes nothing.', 'features cascade resolve_owner', '{ }'],
  ['export_step', 'query', 'Write a STEP file.', 'path*', '{ "path": "out/bracket.step" }'],
  ['export_stl', 'query', 'Write a binary STL for 3D printing.', 'path* deflection_mm', '{ "path": "out/bracket.stl", "deflection_mm": 0.05 }'],
  ['get_appearance', 'query', 'Read colour / opacity / visibility.', 'features', '{ }'],
  ['list_materials', 'query', 'List library materials, optionally for one category.', 'category', '{ "category": "Metals" }'],
  ['search_materials', 'query', 'Free-text material search; returns ≤ 5 ranked candidates and an ambiguous flag.', 'query*', '{ "query": "black plastic" }'],
  ['get_material', 'query', 'One material’s properties.', 'material_id*', '{ "material_id": "titanium" }'],
  ['set_mesh_detail', 'query', 'Display tessellation: coarse | normal | fine. Geometry and exports unchanged.', 'level*', '{ "level": "coarse" }'],
  ['get_workspace_settings', 'query', 'Units, grid and snapping of the project, plus the global defaults new projects start with.', '', '{ }'],
  ['set_units', 'query', 'Display units and precision (mm, cm, m, in, ft; deg, rad). Display only: geometry stays in mm.', 'linear angular linear_precision angular_precision', '{ "linear": "in" }'],
  ['configure_grid', 'query', 'Grid visibility, plane, spacing, major line interval, extent and origin. Lengths in `unit` (default mm).', 'visible enabled plane spacing major_interval extent origin unit', '{ "spacing": 5, "unit": "mm" }'],
  ['configure_snapping', 'query', 'Snapping master switch, grid / translation / rotation snapping and their increments.', 'enabled grid translation rotation rotation_increment translation_increment translation_increment_from_grid unit angle_unit', '{ "enabled": true, "rotation": true, "rotation_increment": 45 }'],
  ['restore_workspace_defaults', 'query', 'Restore units, grid and/or snapping to your global defaults (or the factory ones).', 'section source', '{ "section": "grid" }'],
  ['snap_calculate', 'query', 'Snap a position, translation or angle, or convert world ↔ grid coordinates. Changes nothing.', 'operation* point angle unit angle_unit force', '{ "operation": "position", "point": [12, 3, 7] }'],
  ['convert_units', 'query', 'Convert a length or an angle between units with the application’s conversion table.', 'value* from* to*', '{ "value": 2, "from": "in", "to": "mm" }'],
  ['create_path', 'edit', 'Editable 3D path (linear, bezier, catmull_rom, bspline). Points are [x,y,z] or { p, in, out, w, h, twist }; spine of sweeps.', 'points* type closed', '{ "type": "catmull_rom", "points": [[0,0,0],[20,10,5],[40,0,10]] }'],
  ['update_path', 'edit', 'Edit a path in place (replace points, add/insert/remove/move/set point ops, convert type, close, reverse, simplify, smooth); dependent geometry regenerates.', 'path* points type closed ops reverse simplify smooth', '{ "path": "path", "ops": [ { "op": "move", "index": 1, "position": [20,15,5] } ] }'],
  ['delete_path', 'edit', 'Delete a path that nothing else uses.', 'path*', '{ "path": "path" }'],
  ['evaluate_path', 'query', 'Length, bounding box, minimum curvature radius, self-intersections and position/tangent at normalized parameters.', 'path* t', '{ "path": "path", "t": [0, 0.5, 1] }'],
  ['create_profile', 'edit', 'Reusable 2D cross-section: circle, ellipse, square, rectangle, triangle, semicircle, polygon, hollow_circle or custom contours with holes.', 'shape* radius radius_x radius_y side width height outer_radius inner_radius orientation segments vertices contours', '{ "shape": "hollow_circle", "outer_radius": 3, "inner_radius": 2 }'],
  ['update_profile', 'edit', 'Change a profile; every sweep using it regenerates.', 'profile* radius width height …', '{ "profile": "profile", "radius": 4 }'],
  ['create_sweep', 'edit', 'Sweep a profile along a path into a solid (editable). Smooth paths give smooth surfaces, linear paths mitered corners. cap_start/cap_end flat|round|point; twist; up.', 'path* profile* cap_start cap_end cap_length twist up tolerance max_sections', '{ "path": "spine", "profile": "pipe", "cap_end": "round" }'],
  ['update_sweep', 'edit', 'Change a sweep\'s caps, twist, tolerance or orientation.', 'sweep* cap_start cap_end cap_length twist up tolerance max_sections', '{ "sweep": "sweep", "twist": 90 }'],
  ['create_tube', 'edit', 'Pipe / cable / duct along a path (or inline points): hollow with inner_diameter or wall_thickness, solid otherwise.', 'outer_diameter* path points path_type closed inner_diameter wall_thickness cap_mode', '{ "points": [[0,0,0],[40,0,0],[40,40,20]], "outer_diameter": 8, "wall_thickness": 1 }'],
  ['create_wire', 'edit', 'Solid round wire along a path or inline points; end_style round|flat|point.', 'diameter* path points path_type closed end_style', '{ "points": [[0,0,0],[20,10,5],[40,0,10]], "diameter": 2 }'],
  ['create_ribbon', 'edit', 'Flat strip with thickness along a path; orientation flat|upright; twist in degrees.', 'width* thickness* path points path_type closed orientation twist', '{ "points": [[0,0,0],[50,0,0]], "width": 10, "thickness": 1, "twist": 180 }'],
  ['validate_geometry', 'query', 'Slicer-style check of a body: valid B-Rep, volume, watertight / non-manifold, components, bounding box.', 'object deflection', '{ "object": "tube" }'],
  ['vectorize_image', 'query', 'Trace an image (PNG/JPEG) or SVG into vector shapes without changing the model; reports regions, holes, strokes, widths and warnings. Modes: filled_regions, centerline, outline. Deterministic, no AI.', 'image_id path svg mode threshold adaptive invert source min_area smooth tolerance corner_angle close_gaps px_per_mm width_mm height_mm dpi preview', '{ "path": "flower.png", "mode": "centerline", "width_mm": 60 }'],
  ['image_to_paths', 'edit', 'Trace an image or SVG into editable geometry: a sketch of regions (filled_regions), closed Bezier paths (outline) or stroke paths with width (centerline). The real size is required.', 'image_id path svg mode width_mm height_mm px_per_mm dpi plane offset x y center group threshold adaptive invert min_area smooth tolerance', '{ "path": "logo.png", "mode": "filled_regions", "width_mm": 40 }'],
  ['create_relief', 'edit', 'Emboss (raise) or deboss / engrave (recess) a sketch or solid on a body, editable. mirror flips a sketch for stamps. Errors RELIEF_DISCONNECTED / RELIEF_NO_EFFECT say what to fix.', 'source* target* op* depth offset region mirror reverse', '{ \"source\": \"logo\", \"target\": \"plate\", \"op\": \"emboss\", \"depth\": 1.5, \"mirror\": true }'],
  ['engrave_surface', 'edit', 'Engrave a groove (or raise a bead) along a path on a body: round, v or flat, width and depth. Creates profile, sweep and relief.', 'target* width* path points path_type closed depth style op surface_normal', '{ \"target\": \"plate\", \"points\": [[10,10,4],[50,30,4]], \"width\": 1.2 }'],
  ['create_stamp', 'edit', 'Printable stamp from an image, SVG or sketch: plate (rectangle or circle), raised design, optional handle, rounded edges, mirrored relief, shrinkage compensation. Editable nodes under one id.', 'width height diameter shape base_thickness relief_height path image_id svg sketch mode stroke_width margin mirror rounded_edges handle_enabled handle_height shrinkage_percent', '{ \"path\": \"flower.png\", \"width\": 60, \"height\": 40, \"relief_height\": 2, \"shrinkage_percent\": 12 }'],
  ['apply_recipe', 'edit', 'Create geometry from a declarative JSON recipe (type stamp). Deterministic; stored as a node so it can be edited and reused.', 'recipe*', '{ \"recipe\": { \"type\": \"stamp\", \"base\": { \"width\": 60, \"height\": 40 }, \"design\": { \"source\": { \"path\": \"logo.svg\" } } } }'],
  ['update_recipe', 'edit', 'Edit a stored recipe with a JSON merge patch and regenerate its geometry (one undo step).', 'recipe* changes*', '{ \"recipe\": \"stamp_recipe\", \"changes\": { \"design\": { \"relief_height\": 2 } } }'],
  ['get_recipe', 'query', 'Read a stored recipe (canonical JSON).', 'recipe*', '{ \"recipe\": \"stamp_recipe\" }'],
  ['validate_printability', 'query', 'Can it be 3D printed? Watertight, one piece, no feature thinner than the nozzle (wall thickness by ray casting), optional bed fit; stamps also check ridge width and aspect.', 'object nozzle_mm min_wall_mm bed deflection', '{ \"object\": \"stamp\", \"nozzle_mm\": 0.4 }'],
  ['stamp_impression', 'query', 'Preview what a stamp prints: the physical relief and the mirrored impression it leaves in clay (SVG).', 'stamp*', '{ \"stamp\": \"stamp\" }'],
  ['create_text_geometry', 'edit', 'Create 3D lettering from a font: a sketch of the text (counters kept), optionally extruded, embossed onto or engraved into a body. Size is required; mirror for stamps; text can follow a path.', 'text* size* mode depth target font spacing line_spacing align mirror kerning plane offset face x y center path start', '{ \"text\": \"Simon\", \"size\": 12, \"mode\": \"emboss\", \"depth\": 1.5, \"target\": \"plate\", \"offset\": 4, \"center\": true }'],
  ['update_text', 'edit', 'Edit lettering from create_text_geometry (words, size, font, spacing, alignment, mirror, placement); the sketch regenerates in place and anything built on it follows.', 'sketch* text size font spacing line_spacing align mirror kerning plane offset x y center path start', '{ \"sketch\": \"text_simon\", \"text\": \"Hello\", \"size\": 14 }'],
  ['project_path_to_surface', 'edit', 'Drop a path onto a body: planar (along dir), normal (nearest point) or conform (extra points so it hugs curves). Creates a new path; offset lifts it along the normal. SURFACE_PROJECTION_FAILED lists the points that miss.', 'target* path points path_type mode dir offset tolerance deflection', '{ \"path\": \"line\", \"target\": \"drum\", \"mode\": \"conform\", \"dir\": [-1, 0, 0] }'],
  ['sketch_to_solid', 'edit', 'Drawing or silhouette (PNG/JPEG/SVG) to an extruded solid in one step: trace, sketch, extrude, editable and grouped. Real size and depth are required; strokes style thickens line work. Proposed for approval; lists assumed defaults.', 'path image_id svg depth* style stroke_width width_mm height_mm px_per_mm dpi threshold invert plane offset x y center symmetric', '{ \"path\": \"bracket.png\", \"width_mm\": 60, \"depth\": 5 }'],
  ['check_solid_vs_trace', 'query', 'Critic for sketch_to_solid: compares the solid with the traced area x depth and the drawing footprint; reports each check with numbers.', 'solid* tolerance', '{ \"solid\": \"bracket\" }'],
  ['refresh_projection', 'edit', 'Re-run a projected path with its saved settings after the target body or source path changed; engravings and sweeps on it regenerate.', 'path*', '{ \"path\": \"line_projected\" }'],
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
