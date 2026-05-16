//go:build integration

package storage

import (
	"github.com/bds421/rho-kit/infra/storage/storagetest/v2"
)

// StartS3 returns an [s3backend.Config] pointing at a shared LocalStack
// container.
//
// This is a zero-cost re-export of [storagetest.StartS3].
var StartS3 = storagetest.StartS3

// StartSFTP returns an [sftpbackend.Config] pointing at a shared atmoz/sftp
// container.
//
// This is a zero-cost re-export of [storagetest.StartSFTP].
var StartSFTP = storagetest.StartSFTP
