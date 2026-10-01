package adapterhost

import "github.com/dltkddnr04/integrated-recorder/internal/adapterproto"

// DescriptorFingerprint returns the Core's semantic descriptor fingerprint.
// Presentation-only branding is excluded, matching the restart-stability
// check used by the adapter supervisor.
func DescriptorFingerprint(descriptor adapterproto.Descriptor) string {
	return descriptorFingerprint(descriptor)
}
