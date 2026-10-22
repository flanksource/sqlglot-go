package dbtarget

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = DescribeTable("SQLite file URI paths", func(path, expected string) {
	Expect(sqliteFileURL(path).String()).To(Equal(expected))
},
	Entry("Windows drive", "C:/temp/tenant #1.db", "file:///C:/temp/tenant%20%231.db"),
	Entry("POSIX root", "/var/data/tenant #1.db", "file:///var/data/tenant%20%231.db"),
)
