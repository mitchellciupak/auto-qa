package scenario

import (
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestBuildTestSuiteConfigMapName_UsesFallbackWhenSanitizedEmpty(t *testing.T) {
	name := buildTestSuiteConfigMapName("___", "!!!")
	if name == "" {
		t.Fatal("expected non-empty configmap name")
	}
	if len(name) > 63 {
		t.Fatalf("expected configmap name length <= 63, got %d (%q)", len(name), name)
	}
	pattern := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	if !pattern.MatchString(name) {
		t.Fatalf("configmap name %q is not DNS-compatible", name)
	}
	if !strings.HasPrefix(name, "files-") {
		t.Fatalf("expected fallback prefix files-, got %q", name)
	}
}

func TestBuildTestSuiteConfigMapName_TruncatesLongBaseWithHashSuffix(t *testing.T) {
	scenarioName := strings.Repeat("scenario", 12)
	testSuiteName := strings.Repeat("suite", 12)

	name := buildTestSuiteConfigMapName(scenarioName, testSuiteName)
	if len(name) > 63 {
		t.Fatalf("expected configmap name length <= 63, got %d (%q)", len(name), name)
	}
	pattern := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	if !pattern.MatchString(name) {
		t.Fatalf("configmap name %q is not DNS-compatible", name)
	}
	if !regexp.MustCompile(`-[a-f0-9]{10}$`).MatchString(name) {
		t.Fatalf("expected hash suffix, got %q", name)
	}
}

func TestBuildTestSuiteConfigMapName_StableForSameInput(t *testing.T) {
	a := buildTestSuiteConfigMapName("___", "!!!")
	b := buildTestSuiteConfigMapName("___", "!!!")
	if a != b {
		t.Fatalf("expected deterministic name, got %q and %q", a, b)
	}
}

func TestBuildTestSuiteVolumeName_TruncatesAndAppendsHash(t *testing.T) {
	cmName := strings.Repeat("verylongconfigmapname", 8)
	name := buildTestSuiteVolumeName(cmName)

	if len(name) > 63 {
		t.Fatalf("expected volume name length <= 63, got %d (%q)", len(name), name)
	}
	pattern := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	if !pattern.MatchString(name) {
		t.Fatalf("volume name %q is not DNS_LABEL-compatible", name)
	}
	if !regexp.MustCompile(`-[a-f0-9]{10}$`).MatchString(name) {
		t.Fatalf("expected hash suffix on truncated volume name, got %q", name)
	}
}

func TestInjectFilesIntoTestSuite_UsesDedicatedVolumeName(t *testing.T) {
	cmName := strings.Repeat("config-map-name-segment-", 12)
	testSuite := TestSuiteSpec{
		Volumes: []corev1.Volume{{Name: "existing"}},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      "existing",
			MountPath: "/existing",
		}},
		Files: []TestSuiteFileSpec{{
			Src:       "tests/test_hello.py",
			MountPath: "/tests/test_hello.py",
		}},
	}

	updated := injectFilesIntoTestSuite(testSuite, cmName)

	if len(updated.Volumes) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(updated.Volumes))
	}
	if len(updated.VolumeMounts) != 2 {
		t.Fatalf("expected 2 volume mounts, got %d", len(updated.VolumeMounts))
	}

	generatedVolume := updated.Volumes[1]
	if generatedVolume.VolumeSource.ConfigMap == nil {
		t.Fatal("expected generated volume to be ConfigMap-backed")
	}
	if got := generatedVolume.VolumeSource.ConfigMap.LocalObjectReference.Name; got != cmName {
		t.Fatalf("expected generated volume to reference configmap %q, got %q", cmName, got)
	}
	if generatedVolume.Name == cmName {
		t.Fatalf("expected volume name to differ from configmap name when cmName is too long")
	}
	if len(generatedVolume.Name) > 63 {
		t.Fatalf("expected generated volume name length <= 63, got %d (%q)", len(generatedVolume.Name), generatedVolume.Name)
	}

	generatedMount := updated.VolumeMounts[1]
	if generatedMount.Name != generatedVolume.Name {
		t.Fatalf("expected mount to reference generated volume name %q, got %q", generatedVolume.Name, generatedMount.Name)
	}
	if generatedMount.SubPath != "tests-test_hello.py" {
		t.Fatalf("unexpected generated subPath: got %q", generatedMount.SubPath)
	}
}

func TestValidateConfigMapDataKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{name: "valid", key: "tests-test_hello.py", wantErr: false},
		{name: "empty", key: "", wantErr: true},
		{name: "too long", key: strings.Repeat("a", 254), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfigMapDataKey(tc.key)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for key %q, got nil", tc.key)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error for key %q, got %v", tc.key, err)
			}
		})
	}
}
