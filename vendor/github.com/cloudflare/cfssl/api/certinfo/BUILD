load("@io_bazel_rules_go//go:def.bzl", "go_library")

go_library(
    name = "go_default_library",
    srcs = ["certinfo.go"],
    importmap = "k8s.io/kubernetes/vendor/github.com/cloudflare/cfssl/api/certinfo",
    importpath = "github.com/cloudflare/cfssl/api/certinfo",
    visibility = ["//visibility:public"],
    deps = [
        "//vendor/github.com/cloudflare/cfssl/api:go_default_library",
        "//vendor/github.com/cloudflare/cfssl/certinfo:go_default_library",
        "//vendor/github.com/cloudflare/cfssl/log:go_default_library",
    ],
)

filegroup(
    name = "package-srcs",
    srcs = glob(["**"]),
    tags = ["automanaged"],
    visibility = ["//visibility:private"],
)

filegroup(
    name = "all-srcs",
    srcs = [":package-srcs"],
    tags = ["automanaged"],
    visibility = ["//visibility:public"],
)
