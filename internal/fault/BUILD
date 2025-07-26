load("@io_bazel_rules_go//go:def.bzl", "go_library", "go_test")

go_library(
    name = "fault",
    srcs = ["manager.go"],
    importpath = "github.com/openconfig/lemming/internal/fault",
    visibility = ["//:__subpackages__"],
    deps = [
        "//fault",
        "//proto/config",
        "//proto/fault",
        "@com_github_golang_glog//:glog",
    ],
)

go_test(
    name = "fault_test",
    srcs = ["manager_test.go"],
    embed = [":fault"],
    deps = [
        "//fault",
        "//proto/config",
        "//proto/fault",
        "@com_github_google_go_cmp//cmp",
        "@com_github_openconfig_gnoi//system",
        "@org_golang_google_genproto_googleapis_rpc//status",
        "@org_golang_google_grpc//codes",
        "@org_golang_google_grpc//status",
        "@org_golang_google_protobuf//testing/protocmp",
        "@org_golang_google_protobuf//types/known/anypb",
    ],
)
