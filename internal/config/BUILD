load("@io_bazel_rules_go//go:def.bzl", "go_library")

go_library(
    name = "config",
    srcs = ["loader.go"],
    importpath = "github.com/openconfig/lemming/internal/config",
    visibility = ["//:__subpackages__"],
    deps = [
        "//proto/config",
        "@com_github_golang_glog//:glog",
        "@org_golang_google_protobuf//encoding/prototext",
    ],
)
