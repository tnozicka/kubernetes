// This is a generated file. Do not edit directly.

module k8s.io/cluster-bootstrap

go 1.15

require (
	github.com/stretchr/testify v1.4.0
	gopkg.in/square/go-jose.v2 v2.2.2
	k8s.io/api v0.19.2
	k8s.io/apimachinery v0.19.2
	k8s.io/klog/v2 v2.3.0
)

replace (
	github.com/onsi/ginkgo => github.com/openshift/ginkgo v4.5.0-origin.1+incompatible
	gopkg.in/yaml.v2 => gopkg.in/yaml.v2 v2.2.8
	k8s.io/api => ../api
	k8s.io/apimachinery => ../apimachinery
	k8s.io/cluster-bootstrap => ../cluster-bootstrap
	k8s.io/klog/v2 => k8s.io/klog/v2 v2.2.0
)
