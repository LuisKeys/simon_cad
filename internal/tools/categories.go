package tools

import "fmt"

// Category groups tools by domain, finer than a Capability (geometry alone holds most
// tools). It is metadata for docs and, later, for loading tool definitions on demand.
type Category struct {
	Name        string
	Description string // one line, shown in an index of categories
}

// categories lists every category in presentation order.
var categories = []Category{
	{"primitives", "Create basic solids: box, cylinder, sphere, cone, torus."},
	{"import", "Bring in external geometry: STEP files keep their exact solids, validated STL meshes become solid bodies; decimate_mesh reduces their triangles."},
	{"sketch", "Planar sketches and extruding profiles into solids."},
	{"edit", "Modify features and faces: holes, fillet, chamfer, face edits, remove/suppress/rename/delete."},
	{"transform", "Move, rotate, scale, align, mirror, duplicate, pattern, group objects; read and set dimensions and transforms."},
	{"parametric", "Change feature parameters or drive them with expressions."},
	{"components", "Reusable components: create, insert, detach, sync, list, search, open."},
	{"appearance", "Colors, opacity, visibility and materials."},
	{"workspace", "Units, grid, snapping and mesh detail."},
	{"project", "Model summary, distance measurement, STEP/STL export and checkpoints."},
	{"paths", "Editable 3D paths and 2D profiles, sweeping them into solids (tubes, wires, ribbons), raised and engraved reliefs, and geometry validation."},
	{"image", "Turn an attached image into a sketch proposal and then into geometry; trace images and SVG into sketches and paths."},
	{"refine", "Visual refinement against reference images: regions, design parameters, renders."},
}

// toolCategory assigns each tool to a category. A tool missing here has no category;
// TestEveryToolHasACategory keeps this table complete.
var toolCategory = map[string]string{}

func init() {
	for cat, names := range map[string][]string{
		"primitives": {"create_box", "create_cylinder", "create_sphere", "create_cone", "create_torus"},
		"import":     {"import_stl", "import_step", "decimate_mesh"},
		"sketch":     {"create_sketch", "extrude_sketch", "extrude_profile"},
		"edit": {"add_hole", "fillet", "chamfer", "boolean", "face_extrude", "face_inset", "face_offset",
			"remove_feature", "suppress_feature", "rename_feature", "delete_objects", "check_delete", "batch"},
		"transform": {"move_object", "rotate_object", "transform", "scale", "align", "mirror", "duplicate",
			"linear_pattern", "circular_pattern", "group", "ungroup", "resize_object", "set_object_dimension",
			"set_object_proportional_scaling", "get_object_dimensions", "get_object_transform"},
		"parametric": {"modify_parameter", "set_expression"},
		"components": {"create_component", "insert_component", "detach_component", "sync_component",
			"list_components", "search_components", "open_component"},
		"appearance": {"set_color", "set_opacity", "set_visibility", "reset_appearance", "get_appearance",
			"set_material", "remove_material", "set_material_scale", "list_materials", "search_materials", "get_material"},
		"workspace": {"set_units", "convert_units", "configure_grid", "configure_snapping", "snap_calculate",
			"get_workspace_settings", "restore_workspace_defaults", "set_mesh_detail"},
		"project": {"get_model_summary", "measure_distance", "export_step", "export_stl", "restore_checkpoint"},
		"paths": {"create_path", "update_path", "delete_path", "evaluate_path", "create_profile", "update_profile",
			"create_sweep", "update_sweep", "create_tube", "create_wire", "create_ribbon", "validate_geometry",
			"create_relief", "engrave_surface", "create_stamp", "apply_recipe", "update_recipe", "get_recipe", "validate_printability", "stamp_impression", "create_text_geometry", "update_text", "project_path_to_surface", "refresh_projection"},
		"image": {"analyze_image", "create_from_image", "create_sketch_from_image", "extrude_detected_profile",
			"confirm_detected_dimension", "get_sketch_proposal", "update_sketch_proposal", "set_sketch_scale",
			"vectorize_image", "image_to_paths", "sketch_to_solid", "check_solid_vs_trace"},
		"refine": {"define_region", "lock_region", "unlock_region", "render_model", "calibrate_reference_image",
			"get_visual_state", "define_parameters", "set_parameter", "bind_parameters"},
	} {
		for _, n := range names {
			if prev, dup := toolCategory[n]; dup {
				panic(fmt.Sprintf("tool %s is in categories %s and %s", n, prev, cat))
			}
			toolCategory[n] = cat
		}
	}
}

// Categories returns every category in presentation order.
func Categories() []Category { return append([]Category(nil), categories...) }

// CategoryOf names the category of a tool ("" when it has none).
func CategoryOf(tool string) string { return toolCategory[tool] }

// ByCategory returns the tools of a category in registration order.
func (r *Registry) ByCategory(category string) []*Tool {
	var out []*Tool
	for _, n := range r.order {
		if t := r.tools[n]; t.Category == category {
			out = append(out, t)
		}
	}
	return out
}

// coreTools are the tools kept loaded in every turn when tool definitions load on demand:
// the everyday ones, so most requests need no extra step to reach the tool they want.
var coreTools = map[string]bool{
	"get_model_summary": true,
	"create_box":        true, "create_cylinder": true, "create_sphere": true,
	"create_sketch": true, "extrude_profile": true,
	"add_hole": true, "fillet": true, "delete_objects": true,
	"move_object": true, "rotate_object": true,
	"set_color": true, "measure_distance": true,
}

// IsCore reports whether a tool is always loaded.
func IsCore(tool string) bool { return coreTools[tool] }
