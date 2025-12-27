package resolver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s-lsp/pkg/config"
	"k8s-lsp/pkg/indexer"

	"gopkg.in/yaml.v3"
)

func TestResolveDefinition(t *testing.T) {
	// 1. Setup Config
	cfg := &config.Config{
		References: []config.Reference{
			{
				Name:       "service-ref",
				Symbol:     "k8s.resource.name",
				TargetKind: "Service",
				Match: config.ReferenceMatch{
					Kinds: []string{"Deployment"},
					Path:  "spec.template.spec.containers.env.valueFrom.configMapKeyRef.name",
				},
			},
		},
	}

	// 2. Setup Store
	store := indexer.NewStore()
	serviceRes := &indexer.K8sResource{
		Kind:      "Service",
		Name:      "my-service",
		Namespace: "default",
		FilePath:  "/tmp/service.yaml",
		Line:      0,
		Col:       0,
	}
	store.Add(serviceRes)

	// 3. Create Resolver
	r := NewResolver(store, cfg)

	// 4. Test Content
	yamlContent := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deployment
spec:
  template:
    spec:
      containers:
      - name: my-container
        env:
        - name: MY_CONFIG
          valueFrom:
            configMapKeyRef:
              name: my-service
              key: some-key
`
	// line 14 is "              name: my-service" (because of leading newline)
	// indentation is 14 spaces. "name: " is 6 chars.
	// So "my-service" starts at col 20 (0-based).
	line := 14
	col := 20

	// 5. Call ResolveDefinition
	locs, err := r.ResolveDefinition(yamlContent, "file:///tmp/deployment.yaml", line, col)

	// 6. Assertions
	if err != nil {
		t.Fatalf("ResolveDefinition failed: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("Expected 1 location, got %d", len(locs))
	}
	if locs[0].TargetURI != "file:///tmp/service.yaml" {
		t.Errorf("Expected TargetURI file:///tmp/service.yaml, got %s", locs[0].TargetURI)
	}
	if locs[0].TargetRange.Start.Line != 0 {
		t.Errorf("Expected TargetRange.Start.Line 0, got %d", locs[0].TargetRange.Start.Line)
	}
}

func TestResolveDefinition_Self(t *testing.T) {
	// 1. Setup Config
	cfg := &config.Config{
		Symbols: []config.Symbol{
			{
				Name: "k8s.service",
				Definitions: []config.SymbolDefinition{
					{
						Kinds: []string{"Service"},
						Path:  "metadata.name",
					},
				},
			},
		},
	}

	// 2. Setup Store (empty is fine, or with self)
	store := indexer.NewStore()

	// 3. Create Resolver
	r := NewResolver(store, cfg)

	// 4. Test Content
	yamlContent := `
apiVersion: v1
kind: Service
metadata:
  name: my-service
spec:
  ports:
  - port: 80
`
	// Line 0: empty
	// Line 1: apiVersion
	// Line 2: kind
	// Line 3: metadata
	// Line 4:   name: my-service

	// "  name: my-service"
	// Indent 2. "name: " 6.
	// "my-service" starts at col 8.
	line := 4
	col := 8

	// 5. Call ResolveDefinition
	locs, err := r.ResolveDefinition(yamlContent, "file:///tmp/service.yaml", line, col)

	// 6. Assertions
	if err != nil {
		t.Fatalf("ResolveDefinition failed: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("Expected 1 location, got %d", len(locs))
	}
	if locs[0].TargetURI != "file:///tmp/service.yaml" {
		t.Errorf("Expected TargetURI file:///tmp/service.yaml, got %s", locs[0].TargetURI)
	}
	// TargetRange should be the range of "my-service"
	if locs[0].TargetRange.Start.Line != 4 {
		t.Errorf("Expected TargetRange.Start.Line 4, got %d", locs[0].TargetRange.Start.Line)
	}
}

func TestResolveDefinition_PVCVolumeName_ToPV(t *testing.T) {
	cfg := &config.Config{
		References: []config.Reference{
			{
				Name:       "pvc.volumeName.pv",
				Symbol:     "k8s.resource.name",
				TargetKind: "PersistentVolume",
				Match: config.ReferenceMatch{
					Kinds: []string{"PersistentVolumeClaim"},
					Path:  "spec.volumeName",
				},
			},
		},
	}

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{
		Kind:     "PersistentVolume",
		Name:     "pv-test",
		// PV is cluster-scoped; Store will map empty namespace to "default".
		Namespace: "",
		FilePath:  "/tmp/pv.yaml",
		Line:      3,
		Col:       8,
	})

	r := NewResolver(store, cfg)

	yamlContent := `
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pvc-test
  namespace: dev
spec:
  volumeName: pv-test
`

	// Line 7 is "  volumeName: pv-test" (because of leading newline)
	line := 7
	col := 15

	locs, err := r.ResolveDefinition(yamlContent, "file:///tmp/pvc.yaml", line, col)
	if err != nil {
		t.Fatalf("ResolveDefinition failed: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("Expected 1 location, got %d", len(locs))
	}
	if locs[0].TargetURI != "file:///tmp/pv.yaml" {
		t.Errorf("Expected TargetURI file:///tmp/pv.yaml, got %s", locs[0].TargetURI)
	}
}

func TestResolveReferences(t *testing.T) {
	// 1. Setup Config
	cfg := &config.Config{
		References: []config.Reference{
			{
				Name:       "service-ref",
				Symbol:     "k8s.resource.name",
				TargetKind: "Service",
				Match: config.ReferenceMatch{
					Kinds: []string{"Deployment"},
					Path:  "spec.template.spec.containers.env.valueFrom.configMapKeyRef.name",
				},
			},
		},
	}

	// 2. Setup Store
	store := indexer.NewStore()

	// Add Service definition
	serviceRes := &indexer.K8sResource{
		Kind:      "Service",
		Name:      "my-service",
		Namespace: "default",
		FilePath:  "/tmp/service.yaml",
		// Match the location of "my-service" in yamlContent below (0-based line/col).
		Line:      4,
		Col:       8,
	}
	store.Add(serviceRes)

	// Add Deployment referencing the Service
	// We need to simulate that the indexer has already found the reference.
	// The Store stores references in the resource that HAS the reference?
	// No, Store.FindReferences(kind, name) searches all resources for references.
	// So we need to add a Deployment resource to the store, and it must have the reference in its `References` list.

	deploymentRes := &indexer.K8sResource{
		Kind:      "Deployment",
		Name:      "my-deployment",
		Namespace: "default",
		FilePath:  "/tmp/deployment.yaml",
		Line:      0,
		Col:       0,
		References: []indexer.Reference{
			{
				Kind:   "Service",
				Name:   "my-service",
				Symbol: "k8s.resource.name",
				Line:   14, // Matches the line in TestResolveDefinition
				Col:    20,
			},
		},
	}
	store.Add(deploymentRes)

	// 3. Create Resolver
	r := NewResolver(store, cfg)

	// 4. Test Content (The Service file)
	yamlContent := `
apiVersion: v1
kind: Service
metadata:
  name: my-service
spec:
  ports:
  - port: 80
`
	// Line 4:   name: my-service
	line := 4
	col := 8

	// 5. Call ResolveReferences
	locs, err := r.ResolveReferences(yamlContent, "file:///tmp/service.yaml", line, col)

	// 6. Assertions
	if err != nil {
		t.Fatalf("ResolveReferences failed: %v", err)
	}
	// Should find the reference in Deployment (the definition location at the cursor is filtered out).
	if len(locs) != 1 {
		t.Fatalf("Expected 1 location, got %d", len(locs))
	}

	// Check if we found the deployment reference
	foundDeployment := false
	for _, loc := range locs {
		if loc.URI == "file:///tmp/deployment.yaml" {
			foundDeployment = true
			if loc.Range.Start.Line != 14 {
				t.Errorf("Expected reference at line 14, got %d", loc.Range.Start.Line)
			}
		}
	}
	if !foundDeployment {
		t.Error("Did not find reference in deployment.yaml")
	}
}

func TestResolveLabelReferences(t *testing.T) {
	// 1. Setup Config
	cfg := &config.Config{
		Symbols: []config.Symbol{
			{
				Name: "k8s.label",
				Definitions: []config.SymbolDefinition{
					{
						Kinds: []string{"Pod"},
						Path:  "metadata.labels",
					},
				},
			},
		},
		References: []config.Reference{
			{
				Name:       "service-selector",
				Symbol:     "k8s.label",
				TargetKind: "Pod",
				Match: config.ReferenceMatch{
					Kinds: []string{"Service"},
					Path:  "spec.selector",
				},
			},
		},
	}

	// 2. Setup Store
	store := indexer.NewStore()

	// Add Service referencing the label
	serviceRes := &indexer.K8sResource{
		Kind:      "Service",
		Name:      "my-service",
		Namespace: "default",
		FilePath:  "/tmp/service.yaml",
		Line:      0,
		Col:       0,
		References: []indexer.Reference{
			{
				Kind:   "Pod",
				Name:   "my-app", // The value of the label
				Symbol: "k8s.label",
				Line:   10,
				Col:    10,
			},
		},
	}
	store.Add(serviceRes)

	// 3. Create Resolver
	r := NewResolver(store, cfg)

	// 4. Test Content (The Pod file)
	yamlContent := `
apiVersion: v1
kind: Pod
metadata:
  name: my-pod
  labels:
    app: my-app
spec:
  containers:
  - name: nginx
    image: nginx
`
	// Line 6:     app: my-app
	// Indent 4. "app: " 5.
	// "my-app" starts at col 9.
	line := 6
	col := 9

	// 5. Call ResolveReferences
	locs, err := r.ResolveReferences(yamlContent, "file:///tmp/pod.yaml", line, col)

	// 6. Assertions
	if err != nil {
		t.Fatalf("ResolveReferences failed: %v", err)
	}

	// Should find:
	// 1. The definition itself (Pod label) - Wait, findLabelReferences finds definitions too?
	// Yes, step 1 in findLabelReferences calls Store.FindByLabel.
	// But we haven't added the Pod to the store yet.
	// ResolveReferences doesn't add the current file to the store automatically (unless we do it).
	// But findLabelReferences searches the store.
	// So if we want the definition to be found, we should add the Pod to the store too, or expect it not to be found if not in store.
	// However, usually "Find References" includes the current location if it's a definition.
	// But findLabelReferences implementation:
	// 1. Find definitions (resources having this label) -> Store.FindByLabel
	// 2. Find usages -> Store.FindLabelReferences

	// Since we didn't add the Pod to the store, step 1 will return empty.
	// Step 2 should find the Service.

	if len(locs) != 1 {
		t.Fatalf("Expected 1 location, got %d", len(locs))
	}

	if locs[0].URI != "file:///tmp/service.yaml" {
		t.Errorf("Expected URI file:///tmp/service.yaml, got %s", locs[0].URI)
	}
}

func TestResolveReferences_WorkloadPVCClaimName_ShowsVolumeMountUsages_Deployment(t *testing.T) {
	uri := "file:///tmp/deployment.yaml"
	yamlContent := strings.TrimLeft(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  template:
    spec:
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: mypvc
      containers:
      - name: app
        image: nginx
        volumeMounts:
        - name: data
          mountPath: /data
      - name: sidecar
        image: busybox
        volumeMounts:
        - name: data
          mountPath: /data2
`, "\n")

	assertWorkloadPVCClaimNameReferences(t, uri, yamlContent)
}

func TestResolveReferences_WorkloadPVCClaimName_ShowsVolumeMountUsages_DaemonSet(t *testing.T) {
	uri := "file:///tmp/daemonset.yaml"
	yamlContent := strings.TrimLeft(`
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: demo
spec:
  template:
    spec:
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: mypvc
      containers:
      - name: app
        image: nginx
        volumeMounts:
        - name: data
          mountPath: /data
      - name: sidecar
        image: busybox
        volumeMounts:
        - name: data
          mountPath: /data2
`, "\n")

	assertWorkloadPVCClaimNameReferences(t, uri, yamlContent)
}

func TestResolveReferences_WorkloadPVCClaimName_ShowsVolumeMountUsages_StatefulSet(t *testing.T) {
	uri := "file:///tmp/statefulset.yaml"
	yamlContent := strings.TrimLeft(`
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: demo
spec:
  serviceName: demo
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
    spec:
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: mypvc
      containers:
      - name: app
        image: nginx
        volumeMounts:
        - name: data
          mountPath: /data
      - name: sidecar
        image: busybox
        volumeMounts:
        - name: data
          mountPath: /data2
`, "\n")

	assertWorkloadPVCClaimNameReferences(t, uri, yamlContent)
}

func TestResolveReferences_WorkloadPVCClaimName_ShowsVolumeMountUsages_Job(t *testing.T) {
	uri := "file:///tmp/job.yaml"
	yamlContent := strings.TrimLeft(`
apiVersion: batch/v1
kind: Job
metadata:
  name: demo
spec:
  template:
    spec:
      restartPolicy: Never
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: mypvc
      containers:
      - name: app
        image: nginx
        volumeMounts:
        - name: data
          mountPath: /data
      - name: sidecar
        image: busybox
        volumeMounts:
        - name: data
          mountPath: /data2
`, "\n")

	assertWorkloadPVCClaimNameReferences(t, uri, yamlContent)
}

func TestResolveReferences_WorkloadPVCClaimName_ShowsVolumeMountUsages_CronJob(t *testing.T) {
	uri := "file:///tmp/cronjob.yaml"
	yamlContent := strings.TrimLeft(`
apiVersion: batch/v1
kind: CronJob
metadata:
  name: demo
spec:
  schedule: "*/5 * * * *"
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: Never
          volumes:
          - name: data
            persistentVolumeClaim:
              claimName: mypvc
          containers:
          - name: app
            image: nginx
            volumeMounts:
            - name: data
              mountPath: /data
          - name: sidecar
            image: busybox
            volumeMounts:
            - name: data
              mountPath: /data2
`, "\n")

	assertWorkloadPVCClaimNameReferencesWithClaimPath(
		t,
		uri,
		yamlContent,
		[]string{"spec", "jobTemplate", "spec", "template", "spec", "volumes", "persistentVolumeClaim", "claimName"},
	)
}

func assertWorkloadPVCClaimNameReferences(t *testing.T, uri string, yamlContent string) {
	t.Helper()

	store := indexer.NewStore()
	r := NewResolver(store, &config.Config{})

	// Derive cursor position from parsed YAML to avoid brittle line/col counting.
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(yamlContent), &root); err != nil {
		t.Fatalf("failed to parse yaml: %v", err)
	}
	claimNode := findScalarByPath(&root, []string{"spec", "template", "spec", "volumes", "persistentVolumeClaim", "claimName"}, "mypvc")
	if claimNode == nil {
		t.Fatalf("failed to locate claimName node")
	}
	line := claimNode.Line - 1
	col := claimNode.Column - 1

	locs, err := r.ResolveReferences(yamlContent, uri, line, col)
	if err != nil {
		t.Fatalf("ResolveReferences failed: %v", err)
	}
	if len(locs) < 3 {
		t.Fatalf("expected at least 3 locations (volume name + 2 mounts), got %d", len(locs))
	}

	// We expect 1 volume name + at least 2 volumeMount name occurrences for "data".
	foundDataRanges := 0
	for _, loc := range locs {
		if loc.URI != uri {
			continue
		}
		if loc.Range.End.Character-loc.Range.Start.Character == uint32(len("data")) {
			foundDataRanges++
		}
	}
	if foundDataRanges < 3 {
		t.Fatalf("expected >=3 uri-local 'data' ranges (1 volume + 2 mounts), got %d", foundDataRanges)
	}
}

func assertWorkloadPVCClaimNameReferencesWithClaimPath(t *testing.T, uri string, yamlContent string, claimPath []string) {
	t.Helper()

	store := indexer.NewStore()
	r := NewResolver(store, &config.Config{})

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(yamlContent), &root); err != nil {
		t.Fatalf("failed to parse yaml: %v", err)
	}
	claimNode := findScalarByPath(&root, claimPath, "mypvc")
	if claimNode == nil {
		t.Fatalf("failed to locate claimName node")
	}
	line := claimNode.Line - 1
	col := claimNode.Column - 1

	locs, err := r.ResolveReferences(yamlContent, uri, line, col)
	if err != nil {
		t.Fatalf("ResolveReferences failed: %v", err)
	}
	if len(locs) < 3 {
		t.Fatalf("expected at least 3 locations (volume name + 2 mounts), got %d", len(locs))
	}

	foundDataRanges := 0
	for _, loc := range locs {
		if loc.URI != uri {
			continue
		}
		if loc.Range.End.Character-loc.Range.Start.Character == uint32(len("data")) {
			foundDataRanges++
		}
	}
	if foundDataRanges < 3 {
		t.Fatalf("expected >=3 uri-local 'data' ranges (1 volume + 2 mounts), got %d", foundDataRanges)
	}
}

func findScalarByPath(root *yaml.Node, path []string, value string) *yaml.Node {
	// Traverses YAML and returns the first scalar node whose key-path (ignoring sequence indices)
	// matches the given path and has the requested scalar value.
	var found *yaml.Node
	var walk func(n *yaml.Node, p []string)
	walk = func(n *yaml.Node, p []string) {
		if found != nil || n == nil {
			return
		}
		switch n.Kind {
		case yaml.DocumentNode:
			for _, c := range n.Content {
				walk(c, p)
			}
		case yaml.MappingNode:
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				v := n.Content[i+1]
				walk(v, append(p, k.Value))
			}
		case yaml.SequenceNode:
			for _, item := range n.Content {
				walk(item, p)
			}
		case yaml.ScalarNode:
			if len(p) == len(path) {
				match := true
				for i := range p {
					if p[i] != path[i] {
						match = false
						break
					}
				}
				if match && n.Value == value {
					found = n
				}
			}
		}
	}
	walk(root, nil)
	return found
}

func TestResolveDefinition_Label(t *testing.T) {
	// 1. Setup Config
	cfg := &config.Config{
		References: []config.Reference{
			{
				Name:       "service.selector.label",
				Symbol:     "k8s.label",
				TargetKind: "Pod",
				Match: config.ReferenceMatch{
					Kinds: []string{"Service"},
					Path:  "spec.selector",
				},
			},
		},
	}

	// 2. Setup Store
	store := indexer.NewStore()
	// Add a Pod with the label app=mailpit
	podRes := &indexer.K8sResource{
		Kind:      "Pod",
		Name:      "mailpit-pod",
		Namespace: "mailpit",
		FilePath:  "/tmp/pod.yaml",
		Line:      0,
		Col:       0,
		Labels:    map[string]string{"app": "mailpit"},
	}
	store.Add(podRes)

	// 3. Create Resolver
	r := NewResolver(store, cfg)

	// 4. Test Content
	yamlContent := `apiVersion: v1
kind: Service
metadata:
  name: mailpit
  namespace: mailpit
spec:
  ports:
  - port: 1025
    targetPort: 1025
    name: smtp
  selector:
    app: mailpit`

	// Line 11: "    app: mailpit"
	// 0-based line index: 11
	// "    app: " is 9 chars. "mailpit" starts at col 9.
	line := 11
	col := 9

	// 5. Call ResolveDefinition
	locs, err := r.ResolveDefinition(yamlContent, "file:///tmp/service.yaml", line, col)

	// 6. Assertions
	if err != nil {
		t.Fatalf("ResolveDefinition failed: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("Expected 1 location, got %d", len(locs))
	}
	if locs[0].TargetURI != "file:///tmp/pod.yaml" {
		t.Errorf("Expected TargetURI file:///tmp/pod.yaml, got %s", locs[0].TargetURI)
	}
}

func TestResolveDefinition_VolumeMountName_GoesToVolumesName(t *testing.T) {
	uri := "file:///tmp/deployment.yaml"
	yamlContent := strings.TrimLeft(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  template:
    spec:
      volumes:
      - name: vector-config
        configMap:
          name: vector-config
      containers:
      - name: app
        image: nginx
        volumeMounts:
        - name: vector-config
          mountPath: /etc/vector/vector.yaml
`, "\n")

	store := indexer.NewStore()
	r := NewResolver(store, &config.Config{})

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(yamlContent), &root); err != nil {
		t.Fatalf("failed to parse yaml: %v", err)
	}
	mountNameNode := findScalarByPath(&root, []string{"spec", "template", "spec", "containers", "volumeMounts", "name"}, "vector-config")
	if mountNameNode == nil {
		t.Fatalf("failed to locate volumeMounts.name node")
	}
	line := mountNameNode.Line - 1
	col := mountNameNode.Column - 1

	locs, err := r.ResolveDefinition(yamlContent, uri, line, col)
	if err != nil {
		t.Fatalf("ResolveDefinition failed: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("expected 1 location, got %d", len(locs))
	}
	if locs[0].TargetURI != uri {
		t.Fatalf("expected TargetURI %s, got %s", uri, locs[0].TargetURI)
	}

	volNameNode := findScalarByPath(&root, []string{"spec", "template", "spec", "volumes", "name"}, "vector-config")
	if volNameNode == nil {
		t.Fatalf("failed to locate volumes.name node")
	}
	if int(locs[0].TargetRange.Start.Line) != volNameNode.Line-1 {
		t.Fatalf("expected TargetRange line %d, got %d", volNameNode.Line-1, locs[0].TargetRange.Start.Line)
	}
}

func TestResolveReferences_VolumeMountSubPath_ShowsConfigMapKeyAndEmbeddedFile(t *testing.T) {
	workloadURI := "file:///tmp/deployment.yaml"
	ns := "default"
	cmName := "vector-config"
	key := "iphone-ingress.yaml"

	cmYaml := strings.TrimLeft(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: vector-config
  namespace: default
data:
  iphone-ingress.yaml: |-
    foo: bar
`, "\n")

	tmpDir := t.TempDir()
	cmPath := filepath.Join(tmpDir, "configmap.yaml")
	if err := os.WriteFile(cmPath, []byte(cmYaml), 0o644); err != nil {
		t.Fatalf("failed to write temp configmap: %v", err)
	}

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{
		Kind:      "ConfigMap",
		Name:      cmName,
		Namespace: ns,
		FilePath:  cmPath,
		Line:      0,
		Col:       0,
	})
	r := NewResolver(store, &config.Config{})

	workloadYaml := strings.TrimLeft(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
  namespace: default
spec:
  template:
    spec:
      volumes:
      - name: vector-config
        configMap:
          name: vector-config
      containers:
      - name: app
        image: nginx
        volumeMounts:
        - name: vector-config
          mountPath: /etc/vector/vector.yaml
          subPath: iphone-ingress.yaml
`, "\n")

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(workloadYaml), &root); err != nil {
		t.Fatalf("failed to parse yaml: %v", err)
	}
	subPathNode := findScalarByPath(&root, []string{"spec", "template", "spec", "containers", "volumeMounts", "subPath"}, key)
	if subPathNode == nil {
		t.Fatalf("failed to locate volumeMounts.subPath node")
	}
	line := subPathNode.Line - 1
	col := subPathNode.Column - 1

	locs, err := r.ResolveReferences(workloadYaml, workloadURI, line, col)
	if err != nil {
		t.Fatalf("ResolveReferences failed: %v", err)
	}
	if len(locs) < 1 {
		t.Fatalf("expected at least 1 location, got %d", len(locs))
	}

	foundKeyDef := false
	foundEmbedded := false
	for _, loc := range locs {
		if loc.URI == "file://"+cmPath {
			foundKeyDef = true
		}
		if strings.HasPrefix(loc.URI, "k8s-embedded://") {
			foundEmbedded = true
		}
	}
	if !foundKeyDef {
		t.Fatalf("expected to find ConfigMap key definition location")
	}
	if !foundEmbedded {
		t.Fatalf("expected to find embedded virtual file location")
	}
}

func TestResolveReferences_VolumeMountSubPath_RespectsItemsPathMapping(t *testing.T) {
	workloadURI := "file:///tmp/deployment.yaml"
	ns := "default"
	cmName := "vector-config"
	key := "iphone-ingress.yaml"
	fileName := "mapped.yaml"

	cmYaml := strings.TrimLeft(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: vector-config
  namespace: default
data:
  iphone-ingress.yaml: |-
    foo: bar
`, "\n")

	tmpDir := t.TempDir()
	cmPath := filepath.Join(tmpDir, "configmap.yaml")
	if err := os.WriteFile(cmPath, []byte(cmYaml), 0o644); err != nil {
		t.Fatalf("failed to write temp configmap: %v", err)
	}

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{
		Kind:      "ConfigMap",
		Name:      cmName,
		Namespace: ns,
		FilePath:  cmPath,
		Line:      0,
		Col:       0,
	})
	r := NewResolver(store, &config.Config{})

	workloadYaml := strings.TrimLeft(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
  namespace: default
spec:
  template:
    spec:
      volumes:
      - name: vector-config
        configMap:
          name: vector-config
          items:
          - key: iphone-ingress.yaml
            path: mapped.yaml
      containers:
      - name: app
        image: nginx
        volumeMounts:
        - name: vector-config
          mountPath: /etc/vector/vector.yaml
          subPath: mapped.yaml
`, "\n")

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(workloadYaml), &root); err != nil {
		t.Fatalf("failed to parse yaml: %v", err)
	}
	subPathNode := findScalarByPath(&root, []string{"spec", "template", "spec", "containers", "volumeMounts", "subPath"}, fileName)
	if subPathNode == nil {
		t.Fatalf("failed to locate volumeMounts.subPath node")
	}
	line := subPathNode.Line - 1
	col := subPathNode.Column - 1

	locs, err := r.ResolveReferences(workloadYaml, workloadURI, line, col)
	if err != nil {
		t.Fatalf("ResolveReferences failed: %v", err)
	}

	// Should resolve to the ConfigMap key definition for "iphone-ingress.yaml" and offer an embedded target for that key.
	foundKeyDef := false
	foundEmbeddedForKey := false
	for _, loc := range locs {
		if loc.URI == "file://"+cmPath {
			foundKeyDef = true
		}
		if strings.HasPrefix(loc.URI, "k8s-embedded://") && strings.Contains(loc.URI, "/"+key+"?") {
			foundEmbeddedForKey = true
		}
	}
	if !foundKeyDef {
		t.Fatalf("expected to find ConfigMap key definition location")
	}
	if !foundEmbeddedForKey {
		t.Fatalf("expected to find embedded target for key %q", key)
	}
}

func TestResolveReferences_VolumeMountSubPath_Secret_ShowsKeyAndEmbeddedFile(t *testing.T) {
	workloadURI := "file:///tmp/deployment.yaml"
	ns := "default"
	secName := "obsidian-vault"
	key := "iphone-ingress.yaml"

	secYaml := strings.TrimLeft(`
apiVersion: v1
kind: Secret
metadata:
  name: obsidian-vault
  namespace: default
stringData:
  iphone-ingress.yaml: |-
    hello: world
`, "\n")

	tmpDir := t.TempDir()
	secPath := filepath.Join(tmpDir, "secret.yaml")
	if err := os.WriteFile(secPath, []byte(secYaml), 0o644); err != nil {
		t.Fatalf("failed to write temp secret: %v", err)
	}

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{
		Kind:      "Secret",
		Name:      secName,
		Namespace: ns,
		FilePath:  secPath,
		Line:      0,
		Col:       0,
	})
	r := NewResolver(store, &config.Config{})

	workloadYaml := strings.TrimLeft(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
  namespace: default
spec:
  template:
    spec:
      volumes:
      - name: secret-vol
        secret:
          secretName: obsidian-vault
      containers:
      - name: app
        image: nginx
        volumeMounts:
        - name: secret-vol
          mountPath: /vault
          subPath: iphone-ingress.yaml
`, "\n")

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(workloadYaml), &root); err != nil {
		t.Fatalf("failed to parse yaml: %v", err)
	}
	subPathNode := findScalarByPath(&root, []string{"spec", "template", "spec", "containers", "volumeMounts", "subPath"}, key)
	if subPathNode == nil {
		t.Fatalf("failed to locate volumeMounts.subPath node")
	}
	line := subPathNode.Line - 1
	col := subPathNode.Column - 1

	locs, err := r.ResolveReferences(workloadYaml, workloadURI, line, col)
	if err != nil {
		t.Fatalf("ResolveReferences failed: %v", err)
	}
	if len(locs) < 1 {
		t.Fatalf("expected at least 1 location, got %d", len(locs))
	}

	foundKeyDef := false
	foundEmbedded := false
	for _, loc := range locs {
		if loc.URI == "file://"+secPath {
			foundKeyDef = true
		}
		if strings.HasPrefix(loc.URI, "k8s-embedded://") {
			foundEmbedded = true
		}
	}
	if !foundKeyDef {
		t.Fatalf("expected to find Secret key definition location")
	}
	if !foundEmbedded {
		t.Fatalf("expected to find embedded virtual file location")
	}
}

func TestResolveReferences_VolumeMountSubPath_ProjectedConfigMap_ShowsKeyAndEmbeddedFile(t *testing.T) {
	workloadURI := "file:///tmp/deployment.yaml"
	ns := "default"
	cmName := "vector-config"
	key := "iphone-ingress.yaml"

	cmYaml := strings.TrimLeft(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: vector-config
  namespace: default
data:
  iphone-ingress.yaml: |-
    foo: bar
`, "\n")

	tmpDir := t.TempDir()
	cmPath := filepath.Join(tmpDir, "configmap.yaml")
	if err := os.WriteFile(cmPath, []byte(cmYaml), 0o644); err != nil {
		t.Fatalf("failed to write temp configmap: %v", err)
	}

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "ConfigMap", Name: cmName, Namespace: ns, FilePath: cmPath, Line: 0, Col: 0})
	r := NewResolver(store, &config.Config{})

	workloadYaml := strings.TrimLeft(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
  namespace: default
spec:
  template:
    spec:
      volumes:
      - name: proj
        projected:
          sources:
          - configMap:
              name: vector-config
      containers:
      - name: app
        image: nginx
        volumeMounts:
        - name: proj
          mountPath: /etc/vector
          subPath: iphone-ingress.yaml
`, "\n")

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(workloadYaml), &root); err != nil {
		t.Fatalf("failed to parse yaml: %v", err)
	}
	subPathNode := findScalarByPath(&root, []string{"spec", "template", "spec", "containers", "volumeMounts", "subPath"}, key)
	if subPathNode == nil {
		t.Fatalf("failed to locate volumeMounts.subPath node")
	}
	line := subPathNode.Line - 1
	col := subPathNode.Column - 1

	locs, err := r.ResolveReferences(workloadYaml, workloadURI, line, col)
	if err != nil {
		t.Fatalf("ResolveReferences failed: %v", err)
	}

	foundKeyDef := false
	foundEmbedded := false
	for _, loc := range locs {
		if loc.URI == "file://"+cmPath {
			foundKeyDef = true
		}
		if strings.HasPrefix(loc.URI, "k8s-embedded://") {
			foundEmbedded = true
		}
	}
	if !foundKeyDef {
		t.Fatalf("expected to find projected ConfigMap key definition location")
	}
	if !foundEmbedded {
		t.Fatalf("expected to find embedded virtual file location")
	}
}

func TestResolveReferences_VolumeMountSubPath_ProjectedSecret_ShowsKeyAndEmbeddedFile(t *testing.T) {
	workloadURI := "file:///tmp/deployment.yaml"
	ns := "default"
	secName := "obsidian-vault"
	key := "iphone-ingress.yaml"

	secYaml := strings.TrimLeft(`
apiVersion: v1
kind: Secret
metadata:
  name: obsidian-vault
  namespace: default
stringData:
  iphone-ingress.yaml: |-
    hello: world
`, "\n")

	tmpDir := t.TempDir()
	secPath := filepath.Join(tmpDir, "secret.yaml")
	if err := os.WriteFile(secPath, []byte(secYaml), 0o644); err != nil {
		t.Fatalf("failed to write temp secret: %v", err)
	}

	store := indexer.NewStore()
	store.Add(&indexer.K8sResource{Kind: "Secret", Name: secName, Namespace: ns, FilePath: secPath, Line: 0, Col: 0})
	r := NewResolver(store, &config.Config{})

	workloadYaml := strings.TrimLeft(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
  namespace: default
spec:
  template:
    spec:
      volumes:
      - name: proj
        projected:
          sources:
          - secret:
              name: obsidian-vault
      containers:
      - name: app
        image: nginx
        volumeMounts:
        - name: proj
          mountPath: /vault
          subPath: iphone-ingress.yaml
`, "\n")

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(workloadYaml), &root); err != nil {
		t.Fatalf("failed to parse yaml: %v", err)
	}
	subPathNode := findScalarByPath(&root, []string{"spec", "template", "spec", "containers", "volumeMounts", "subPath"}, key)
	if subPathNode == nil {
		t.Fatalf("failed to locate volumeMounts.subPath node")
	}
	line := subPathNode.Line - 1
	col := subPathNode.Column - 1

	locs, err := r.ResolveReferences(workloadYaml, workloadURI, line, col)
	if err != nil {
		t.Fatalf("ResolveReferences failed: %v", err)
	}

	foundKeyDef := false
	foundEmbedded := false
	for _, loc := range locs {
		if loc.URI == "file://"+secPath {
			foundKeyDef = true
		}
		if strings.HasPrefix(loc.URI, "k8s-embedded://") {
			foundEmbedded = true
		}
	}
	if !foundKeyDef {
		t.Fatalf("expected to find projected Secret key definition location")
	}
	if !foundEmbedded {
		t.Fatalf("expected to find embedded virtual file location")
	}
}

func TestEmbeddedContent_SecretData_Base64RoundTrip(t *testing.T) {
	r := NewResolver(indexer.NewStore(), &config.Config{})

	// "hello: world\n" base64
	secretYaml := strings.TrimLeft(`
apiVersion: v1
kind: Secret
metadata:
  name: s
  namespace: default
data:
  file.yaml: aGVsbG86IHdvcmxkCg==
`, "\n")

	got, err := r.ResolveEmbeddedContent(secretYaml, "file.yaml")
	if err != nil {
		t.Fatalf("ResolveEmbeddedContent failed: %v", err)
	}
	if got != "hello: world\n" {
		t.Fatalf("expected decoded content, got %q", got)
	}

	updated, err := r.UpdateEmbeddedContent(secretYaml, "file.yaml", "updated: yes\n")
	if err != nil {
		t.Fatalf("UpdateEmbeddedContent failed: %v", err)
	}

	got2, err := r.ResolveEmbeddedContent(updated, "file.yaml")
	if err != nil {
		t.Fatalf("ResolveEmbeddedContent (updated) failed: %v", err)
	}
	if got2 != "updated: yes" {
		t.Fatalf("expected round-tripped decoded content, got %q", got2)
	}
}
