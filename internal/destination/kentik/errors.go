package kentik

import "fmt"

func errNoKentikID(externalID string) error {
	return fmt.Errorf("kentik: no known Kentik id for external id %q", externalID)
}
