package tools

import "testing"

func TestEveryToolHasACategory(t *testing.T) {
	valid := map[string]bool{}
	for _, c := range Categories() {
		valid[c.Name] = true
	}
	reg := Default()
	for _, n := range reg.Names() {
		tool, _ := reg.Get(n)
		if !valid[tool.Category] {
			t.Errorf("tool %s has category %q; assign it in categories.go", n, tool.Category)
		}
	}
}

func TestCategoryTableOnlyNamesRegisteredTools(t *testing.T) {
	reg := Default()
	for n, c := range toolCategory {
		if _, ok := reg.Get(n); !ok {
			t.Errorf("categories.go puts unknown tool %s in %s", n, c)
		}
	}
}

func TestEveryCategoryHasTools(t *testing.T) {
	reg := Default()
	for _, c := range Categories() {
		if len(reg.ByCategory(c.Name)) == 0 {
			t.Errorf("category %s has no tools", c.Name)
		}
	}
}
