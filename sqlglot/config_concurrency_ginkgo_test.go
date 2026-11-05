package sqlglot

import (
	"fmt"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Concurrent dialect initialization", func() {
	It("shares complete configuration while parsing concurrently on first use of every dialect", func() {
		const workers = 32
		const query = "SELECT 1 WHERE 1 IS NOT NULL"
		type observation struct {
			config *Config
			class  string
			err    error
		}
		for _, dialect := range Dialects() {
			start := make(chan struct{})
			results := make(chan observation, workers)
			var group sync.WaitGroup
			for range workers {
				group.Go(func() {
					<-start
					config, ok := ConfigFor(dialect)
					if !ok || config.Tables == nil || config.trie == nil {
						results <- observation{err: fmt.Errorf("dialect %q has incomplete configuration", dialect)}
						return
					}
					tree, err := ParseOne(query, dialect)
					result := observation{config: config, err: err}
					if err == nil {
						result.class = tree.Class
					}
					results <- result
				})
			}
			close(start)
			group.Wait()
			close(results)
			Expect(results).To(HaveLen(workers), dialect)
			var shared *Config
			for result := range results {
				Expect(result.err).NotTo(HaveOccurred(), dialect)
				Expect(result.class).To(Equal("Select"), dialect)
				if shared == nil {
					shared = result.config
				}
				Expect(result.config).To(BeIdenticalTo(shared), dialect)
			}
		}
	})
})
