use assert_cmd::Command;
use predicates::prelude::PredicateBooleanExt;

#[test]
fn cli_exposes_the_small_v1_surface() {
    Command::cargo_bin("factory")
        .unwrap()
        .arg("--help")
        .assert()
        .success()
        .stdout(predicates::str::contains("\n  run "))
        .stdout(predicates::str::contains("\n  validate "))
        .stdout(predicates::str::contains("\n  daemon ").not())
        .stdout(predicates::str::contains("\n  approve ").not());

    Command::cargo_bin("factory")
        .unwrap()
        .args(["workflow", "--help"])
        .assert()
        .success()
        .stdout(predicates::str::contains("\n  run "))
        .stdout(predicates::str::contains("\n  create ").not());
}
