// This is a generated file. Do not edit directly.

module k8s.io/sample-controller

go 1.15

require (
	k8s.io/api v0.20.0-beta.2
	k8s.io/apimachinery v0.20.0-beta.2
	k8s.io/client-go v0.20.0-beta.2
	k8s.io/code-generator v0.20.0-beta.2
	k8s.io/klog/v2 v2.4.0
)

replace (
	github.com/imdario/mergo => github.com/imdario/mergo v0.3.5
	github.com/onsi/ginkgo => github.com/openshift/onsi-ginkgo v4.5.0-origin.1+incompatible
	gopkg.in/yaml.v2 => gopkg.in/yaml.v2 v2.2.8
	k8s.io/api => ../api
	k8s.io/apimachinery => ../apimachinery
	k8s.io/client-go => ../client-go
	k8s.io/code-generator => ../code-generator
	k8s.io/sample-controller => ../sample-controller
)
