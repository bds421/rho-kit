package apperror_test

import (
	"errors"
	"fmt"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

func ExampleNewNotFound() {
	err := apperror.NewNotFound("user", "u-42")
	fmt.Println(err)
	fmt.Println(apperror.IsNotFound(err))
	// Output:
	// user u-42 not found
	// true
}

func ExampleNewValidation() {
	err := apperror.NewValidation("email is required")
	fmt.Println(err)
	fmt.Println(apperror.IsValidation(err))
	// Output:
	// email is required
	// true
}

func ExampleShouldRetry() {
	retriable := apperror.NewRateLimit("slow down")
	permanent := apperror.NewValidation("bad email")
	fmt.Println(apperror.ShouldRetry(retriable))
	fmt.Println(apperror.ShouldRetry(permanent))
	fmt.Println(apperror.ShouldRetry(errors.New("plain error")))
	// Output:
	// true
	// false
	// false
}
