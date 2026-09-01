# APK versioning design

## Goal

Give each Android release a durable, identifiable APK filename and a monotonically increasing Android version code.

## Version policy

- Maintain `versionName` as a semantic version in `android/app/build.gradle.kts`.
- Increment `versionCode` for every distributable APK so Android accepts it as an upgrade.
- Start the completed catalog and audiobook changes at version `1.1.0`, code `2`.

## Artifact naming

Gradle derives output names from the application version and build type:

`BookBox-<versionName>-<versionCode>-<buildType>.apk`

The first artifact under this policy is `BookBox-1.1.0-2-debug.apk`. Gradle keeps the APK in its normal `build/outputs/apk/<buildType>/` directory.

## Build behavior

The Android Gradle configuration sets the name for every APK variant. No timestamp or machine-local counter enters the filename, so rebuilding a source version produces the same artifact name. A later release changes `versionName` and `versionCode` in one place.

## Verification

Run `:app:assembleDebug`, confirm Gradle succeeds, and verify that the expected versioned APK exists and is newer than the source commit.
