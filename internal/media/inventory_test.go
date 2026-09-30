package media

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// Same wire fixture independently checked by Asset's producer contract tests.
func inventoryFixture() RecordingPackageInventory {
	return RecordingPackageInventory{SchemaVersion: 1, PresetVersion: "hls-v1",
		Objects: []RecordingPackageObject{
			{Path: "master.m3u8", SizeBytes: 100, SHA256: strings.Repeat("a", 64)},
			{Path: "720p/index.m3u8", SizeBytes: 100, SHA256: strings.Repeat("b", 64)},
			{Path: "720p/init.mp4", SizeBytes: 100, SHA256: strings.Repeat("c", 64)},
			{Path: "720p/seg-000000.m4s", SizeBytes: 100, SHA256: strings.Repeat("d", 64)},
		}, Renditions: []RecordingRendition{{Name: "720p", Width: 1280, Height: 720, FrameRate: 30, VideoBitrate: 1_500_000, AudioBitrate: 128_000, DurationSeconds: 5, SegmentCount: 1}}}
}

func TestInventoryMatchesProducerDigestAndLimits(t *testing.T) {
	i := inventoryFixture()
	digest, err := RecordingInventoryDigest(i)
	if err != nil || digest != "b29d5b3efa97e926f585b0c62a480d45465730f16edd8059d4fb16792629e58b" {
		t.Fatalf("producer digest mismatch: %s %v", digest, err)
	}
	i.InventoryDigest = digest
	if _, err := ValidateRecordingInventory(i); err != nil {
		t.Fatal(err)
	}
	i.Objects[0], i.Objects[3] = i.Objects[3], i.Objects[0]
	if other, err := RecordingInventoryDigest(i); err != nil || other != digest {
		t.Fatalf("ordering mismatch: %s %v", other, err)
	}
	for _, path := range []string{"../secret", "/master.m3u8", "720p/%2e%2e/secret", "720p\\init.mp4", "https://evil/init.mp4", "720p/seg-000000.m4s?token=x", "package.json"} {
		bad := inventoryFixture()
		bad.Objects[3].Path = path
		if _, err := ValidateRecordingInventory(bad); err == nil {
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
	for _, mutation := range []func(*RecordingPackageInventory){
		func(i *RecordingPackageInventory) { i.Objects = append(i.Objects, i.Objects[0]) },
		func(i *RecordingPackageInventory) { i.Objects = i.Objects[:3] },
		func(i *RecordingPackageInventory) { i.Objects[0].SizeBytes = 128<<20 + 1 },
		func(i *RecordingPackageInventory) { i.Renditions[0].FrameRate = math.NaN() },
		func(i *RecordingPackageInventory) { i.InventoryDigest = strings.Repeat("f", 64) },
	} {
		bad := inventoryFixture()
		mutation(&bad)
		if _, err := ValidateRecordingInventory(bad); err == nil {
			t.Fatal("accepted invalid inventory")
		}
	}
}

func TestPrepareSourceAndPackageBudgets(t *testing.T) {
	if err := ValidateRecordingSourceSize(50_000_000_000); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecordingSourceSize(50_000_000_001); !errors.Is(err, ErrRecordingSourceTooLarge) {
		t.Fatalf("source limit: %v", err)
	}
	size, err := EstimateRecordingPackageSize(9000, []int64{1_500_000, 3_000_000})
	if err != nil || size != 5_511_015_000 {
		t.Fatalf("2.5h dual estimate: %d %v", size, err)
	}
	if _, err := EstimateRecordingPackageSize(18000, []int64{1_500_000, 3_000_000}); !errors.Is(err, ErrRecordingPackageEstimateTooLarge) {
		t.Fatalf("estimate cap: %v", err)
	}
	if _, err := EstimateRecordingPackageSize(math.Inf(1), []int64{1_500_000}); err == nil {
		t.Fatal("accepted infinite duration")
	}
}
