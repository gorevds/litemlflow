package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
)

// SearchModelVersions loads tags and aliases in batches; spanning more than
// one batch must still attach each row to the right version.
func TestSearchModelVersionsBatchedMeta(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newStore(t)
	const ws = "default"
	if err := st.CreateRegisteredModel(ctx, ws, &model.RegisteredModel{Name: "m"}); err != nil {
		t.Fatal(err)
	}
	const n = 260 // > mvBatchSize (250)
	for i := 1; i <= n; i++ {
		if _, err := st.CreateModelVersion(ctx, ws, &model.ModelVersion{Name: "m", Source: "s"}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetModelVersionTag(ctx, ws, "m", int64(i), "idx", fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetModelAlias(ctx, ws, "m", "first", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.SetModelAlias(ctx, ws, "m", "last", n); err != nil {
		t.Fatal(err)
	}

	res, err := st.SearchModelVersions(ctx, ws, "", 1000, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != n {
		t.Fatalf("want %d versions, got %d", n, len(res.Items))
	}
	for _, mv := range res.Items {
		if len(mv.Tags) != 1 || mv.Tags[0].Value != fmt.Sprint(mv.Version) {
			t.Fatalf("v%d tags = %v", mv.Version, mv.Tags)
		}
		var want []string
		switch mv.Version {
		case 1:
			want = []string{"first"}
		case n:
			want = []string{"last"}
		}
		if fmt.Sprint(mv.Aliases) != fmt.Sprint(want) {
			t.Fatalf("v%d aliases = %v, want %v", mv.Version, mv.Aliases, want)
		}
	}

	rms, err := st.SearchRegisteredModels(ctx, ws, "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rms.Items) != 1 || len(rms.Items[0].Aliases) != 2 ||
		rms.Items[0].Aliases[0] != (model.ModelAlias{Alias: "first", Version: 1}) ||
		rms.Items[0].Aliases[1] != (model.ModelAlias{Alias: "last", Version: n}) {
		t.Fatalf("registered model aliases = %+v", rms.Items)
	}

	latest, err := st.GetLatestModelVersions(ctx, ws, "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 || latest[0].Version != n || len(latest[0].Aliases) != 1 || len(latest[0].Tags) != 1 {
		t.Fatalf("latest = %+v", latest)
	}
}
