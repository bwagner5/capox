package cloud

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// InstanceIDFromProviderID extracts the Oxide instance ID from a provider ID.
func InstanceIDFromProviderID(providerID string) (string, error) {
	if providerID == "" {
		return "", errors.New("provider id is empty")
	}

	if !strings.HasPrefix(providerID, "oxide://") {
		return "", errors.New("provider id does not have 'oxide://' prefix")
	}

	instanceID := strings.TrimPrefix(providerID, "oxide://")

	if _, err := uuid.Parse(instanceID); err != nil {
		return "", fmt.Errorf("provider id contains invalid uuid: %w", err)
	}

	return instanceID, nil
}

// NewProviderID formats an Oxide instance ID as a provider ID.
func NewProviderID(instanceID string) string {
	return fmt.Sprintf("oxide://%s", instanceID)
}
