package templateutil

import (
	"slices"
	"strings"
	"testing"
)

func TestFuncDocsMatchesFuncMap(t *testing.T) {
	funcs := FuncMap()
	docs := FuncDocs()

	if len(docs) != len(funcs) {
		t.Fatalf("want a doc per func: %d funcs, %d docs", len(funcs), len(docs))
	}
	for _, doc := range docs {
		if _, ok := funcs[doc.Name]; !ok {
			t.Errorf("documented %q is not in the func map", doc.Name)
		}
		if !strings.HasPrefix(doc.Usage, doc.Name) {
			t.Errorf("usage %q does not start with %q", doc.Usage, doc.Name)
		}
	}
	for name := range funcs {
		if !slices.ContainsFunc(docs, func(d FuncDoc) bool { return d.Name == name }) {
			t.Errorf("func %q is undocumented", name)
		}
	}
}

func TestFuncHelpAlignsEveryDoc(t *testing.T) {
	help := FuncHelp()

	for _, doc := range FuncDocs() {
		if !strings.Contains(help, doc.Usage) || !strings.Contains(help, doc.Desc) {
			t.Errorf("help is missing %q", doc.Name)
		}
	}
	if !strings.HasSuffix(help, "\n") {
		t.Error("want a trailing newline so the block embeds cleanly")
	}
}
