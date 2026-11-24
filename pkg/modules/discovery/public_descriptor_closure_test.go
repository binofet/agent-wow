package moddisc

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Each module descriptor includes imports. Compiled Go APIs do not detect stale
// copies of an imported public schema embedded in a different module's FDS.
func TestMaintainedPublicDescriptorImportClosure(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "modules", "*", "module.pb"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("public descriptors unavailable: %v", err)
	}
	files := map[string]*descriptorpb.FileDescriptorProto{}
	origins := map[string]string{}
	combined := &descriptorpb.FileDescriptorSet{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		set := &descriptorpb.FileDescriptorSet{}
		if err = proto.Unmarshal(data, set); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if _, err = protodesc.NewFiles(set); err != nil {
			t.Fatalf("%s import closure: %v", path, err)
		}
		for _, file := range set.File {
			name := file.GetName()
			if previous, exists := files[name]; exists {
				if !proto.Equal(previous, file) {
					t.Fatalf("conflicting imported public file %s in %s and %s; regenerate dependent descriptor sets", name, origins[name], path)
				}
			} else {
				files[name] = file
				origins[name] = path
				combined.File = append(combined.File, file)
			}
		}
	}
	if _, err = protodesc.NewFiles(combined); err != nil {
		t.Fatalf("combined public import closure: %v", err)
	}
	t.Logf("validated %d public descriptor roots and %d unique files", len(paths), len(files))
}
