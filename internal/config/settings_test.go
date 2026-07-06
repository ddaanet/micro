package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSoftWrapColumnValidation exercises the validator registered for
// softwrapcolumn (validateNonNegativeValue) via OptionIsValid, the same entry
// point used by verifySetting (parsed settings.json) and SetOptionNative
// (live `set softwrapcolumn N` commands).
func TestSoftWrapColumnValidation(t *testing.T) {
	t.Run("negative value is rejected", func(t *testing.T) {
		err := OptionIsValid("softwrapcolumn", float64(-1))
		assert.Error(t, err)
	})

	t.Run("zero is accepted", func(t *testing.T) {
		err := OptionIsValid("softwrapcolumn", float64(0))
		assert.NoError(t, err)
	})

	t.Run("positive value is accepted", func(t *testing.T) {
		err := OptionIsValid("softwrapcolumn", float64(80))
		assert.NoError(t, err)
	})
}
