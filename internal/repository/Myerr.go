package repository

import "errors"

var (
	ErrNotFound         = errors.New("resource not found")
	ErrAlreadyExists    = errors.New("resource already exists")
	ErrInvalidReference = errors.New("invalid reference")
	ErrStorage          = errors.New("storage failure")
)
