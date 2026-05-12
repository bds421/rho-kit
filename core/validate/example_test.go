package validate_test

import (
	"fmt"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/validate"
)

type signupRequest struct {
	Email string `json:"email" validate:"required,email"`
	Age   int    `json:"age"   validate:"min=18"`
}

func ExampleStruct() {
	v := validate.New()
	err := v.Struct(signupRequest{Email: "not-an-email", Age: 15})
	fmt.Println(apperror.IsValidation(err))
	fmt.Println(err)
	// Output:
	// true
	// email: must be a valid email address; age: must be at least 18
}
