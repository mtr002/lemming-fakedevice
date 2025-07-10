load("@io_bazel_rules_go//go:def.bzl", "go_test")

go_test(
    name = "link_qualification_test",
    size = "enormous",
    srcs = ["link_qualification_test.go"],
    data = ["//integration_tests/twodut_tests:topology_testbed"],
    deps = [
        "//gnmi/fakedevice",
        "//gnmi/oc",
        "//gnmi/oc/ocpath",
        "//internal/binding",
        "@com_github_openconfig_gnoi//packet_link_qualification",
        "@com_github_openconfig_gnoigo//:gnoigo",
        "@com_github_openconfig_ondatra//:ondatra",
        "@com_github_openconfig_ondatra//gnmi",
        "@com_github_openconfig_ygot//ygot",
        "@org_golang_google_protobuf//types/known/durationpb",
    ],
) 