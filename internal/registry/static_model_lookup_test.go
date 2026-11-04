package registry

import (
	"reflect"
	"testing"
)

func TestLookupStaticModelInfoUsesEffectiveMetaCatalog(t *testing.T) {
	modelsCatalogStore.mu.RLock()
	original := modelsCatalogStore.data
	modelsCatalogStore.mu.RUnlock()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = original
		modelsCatalogStore.mu.Unlock()
	})
	for _, model := range GetMetaModels() {
		if got := LookupStaticModelInfo(model.ID); !reflect.DeepEqual(got, model) {
			t.Errorf("lookup metadata differs from the active Meta catalog for %q", model.ID)
		}
	}

	partial := *original
	partial.Meta = []*ModelInfo{{ID: "muse-spark-1.3", ContextLength: 12345}}
	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = &partial
	modelsCatalogStore.mu.Unlock()
	if got := LookupStaticModelInfo(partial.Meta[0].ID); !reflect.DeepEqual(got, partial.Meta[0]) {
		t.Fatalf("lookup did not use active Meta metadata: %+v", got)
	}
	if got := LookupStaticModelInfo("muse-spark-1.1"); got != nil {
		t.Fatalf("lookup resurrected a Meta model absent from the active catalog: %+v", got)
	}

	withoutMeta := partial
	withoutMeta.Meta = nil
	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = &withoutMeta
	modelsCatalogStore.mu.Unlock()
	for _, model := range GetMetaModels() {
		if got := LookupStaticModelInfo(model.ID); !reflect.DeepEqual(got, model) {
			t.Errorf("lookup metadata differs from the Meta fallback for %q", model.ID)
		}
	}
}
