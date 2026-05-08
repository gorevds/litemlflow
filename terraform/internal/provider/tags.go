// tags.go — shared helpers for converting between framework types.Map and Go map[string]string.
package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// tagsFromMap converts a framework types.Map (string→string) into a plain Go map.
// Null or unknown maps return nil (no tags). Diagnostics are appended on error.
func tagsFromMap(ctx context.Context, m types.Map, diags *diag.Diagnostics) map[string]string {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	elements := make(map[string]types.String, len(m.Elements()))
	d := m.ElementsAs(ctx, &elements, false)
	diags.Append(d...)
	if d.HasError() {
		return nil
	}
	result := make(map[string]string, len(elements))
	for k, v := range elements {
		result[k] = v.ValueString()
	}
	return result
}
