package httpx

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/bds421/rho-kit/core/apperror"
)

// ParsePathID extracts a path parameter and validates it as a UUID.
// Returns the ID string and true on success, or writes a structured
// validation error and returns false if the value is not a valid UUID.
func ParsePathID(w http.ResponseWriter, r *http.Request, param string) (string, bool) {
	raw := r.PathValue(param)
	if _, err := uuid.Parse(raw); err != nil {
		WriteValidationError(w, nil, apperror.NewFieldValidation(apperror.FieldError{
			Field:   param,
			Message: "invalid UUID format",
		}))
		return "", false
	}
	return raw, true
}
