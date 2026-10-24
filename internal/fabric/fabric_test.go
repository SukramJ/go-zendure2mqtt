// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package fabric

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
)

// facade is the part of go-fabric a daemon embeds: the node, the device kit,
// the host contract and the typed cluster packages. Everything else of the
// module — bridge, store, mdns, im, transport, secure, endpoint, … — is
// below it, and a daemon that needs it has found a facade gap.
func facade(path string) bool {
	const mod = "github.com/SukramJ/go-fabric/"
	if !strings.HasPrefix(path, mod) {
		return true // not go-fabric
	}
	rest := strings.TrimPrefix(path, mod)
	switch rest {
	case "node", "device", "contract":
		return true
	}
	return strings.HasPrefix(rest, "cluster/spec/") && !strings.Contains(strings.TrimPrefix(rest, "cluster/spec/"), "/")
}

// testOnly are the facade packages a test of the projection uses: the
// in-process site and the controller's path builders.
var testOnly = map[string]bool{
	"github.com/SukramJ/go-fabric/fabrictest":        true,
	"github.com/SukramJ/go-fabric/controller/client": true,
}

// TestFabricImportsOnlyTheFacade: the package imports go-fabric's facade
// alone (node, device, contract, cluster/spec/<cluster>; fabrictest and
// controller/client in tests), the rule go-fabric's script/archcheck holds
// for its examples.
func TestFabricImportsOnlyTheFacade(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		src, err := os.ReadFile(name) //nolint:gosec // the package's own files
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if testOnly[path] {
				if !strings.HasSuffix(name, "_test.go") {
					t.Errorf("%s imports %s outside a test", name, path)
				}
				continue
			}
			if !facade(path) {
				t.Errorf("%s imports %s, below go-fabric's facade", name, path)
			}
		}
	}
}

func TestNodeConfigFromTheMatterBlock(t *testing.T) {
	cfg, err := config.Load(strings.NewReader("MQTT_SERVER: broker\nMATTER_ENABLED: true\nMATTER_PASSCODE: 34567890\n"+
		"MATTER_STORE_PATH: "+filepath.Join(t.TempDir(), "fabric.db")+"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	nc := NodeConfig(cfg, nil)
	if err := nc.Validate(); err != nil {
		t.Fatalf("node.Config.Validate: %v", err)
	}
	if nc.Network.Listen != ":5540" || nc.Commissioning.Passcode != 34567890 || nc.Commissioning.Discriminator != 3840 ||
		nc.ProductDescription.VendorID != 0xFFF1 || nc.ProductDescription.ProductID != 0x8001 ||
		nc.BasicInformation.NodeLabel != "zendure2mqtt" || nc.Store.Path != cfg.MatterStorePath {
		t.Errorf("node config %+v", nc)
	}
}

func TestSoftwareVersion(t *testing.T) {
	for v, want := range map[string]uint32{"0.11.0": 11000, "1.2.3": 1002003, "v2.0.10": 2000010, "dev": 0, "1.2": 0, "1.2.x": 0} {
		if got := softwareVersion(v); got != want {
			t.Errorf("softwareVersion(%q) = %d, want %d", v, got, want)
		}
	}
}
