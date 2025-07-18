load("@io_bazel_rules_go//go:def.bzl", "go_test")

go_test(
    name = "fault_test",
    size = "enormous",
    srcs = ["fault_test.go"],
    data = ["//integration_tests/onedut_tests:topology_testbed"],
    deps = [
        "//fault",
        "//internal/binding",
        "@com_github_openconfig_gnmi//proto/gnmi",
        "@com_github_openconfig_gnoi//packet_link_qualification",
        "@com_github_openconfig_gnoi//system",
        "@com_github_openconfig_gnoi//types",
        "@com_github_openconfig_ondatra//:ondatra",
        "@com_github_openconfig_ondatra//gnmi",
        "@com_github_openconfig_testt//:testt",
        "@org_golang_google_grpc//:grpc",
        "@org_golang_google_grpc//codes",
        "@org_golang_google_grpc//status",
    ],
)
