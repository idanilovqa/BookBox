# APK Versioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce Android APKs whose filenames contain the BookBox semantic version, Android version code, and build type.

**Architecture:** Keep the release identity in Android `defaultConfig` and use the Android Components variant API to derive every APK output filename from that identity. Gradle continues to place artifacts in its standard build output directory.

**Tech Stack:** Gradle Kotlin DSL, Android Gradle Plugin, Java 17

## Global Constraints

- Set the first versioned release to `versionName = "1.1.0"` and `versionCode = 2`.
- Name each APK `BookBox-<versionName>-<versionCode>-<buildType>.apk`.
- Do not add timestamps or machine-local counters.
- Preserve the normal `android/app/build/outputs/apk/<buildType>/` location.

---

### Task 1: Version Android APK artifacts

**Files:**
- Modify: `android/app/build.gradle.kts`

**Interfaces:**
- Consumes: Android `defaultConfig.versionName`, `defaultConfig.versionCode`, and each variant's `buildType`.
- Produces: APK filenames in the form `BookBox-1.1.0-2-debug.apk`.

- [ ] **Step 1: Run the pre-change assertion**

```powershell
$buildFile = Get-Content -Raw android/app/build.gradle.kts
if ($buildFile -notmatch 'versionCode\s*=\s*2' -or
    $buildFile -notmatch 'versionName\s*=\s*"1\.1\.0"' -or
    $buildFile -notmatch 'BookBox-') { throw 'Versioned APK configuration is absent' }
```

Expected: FAIL with `Versioned APK configuration is absent`.

- [ ] **Step 2: Update the version and output naming**

Change `defaultConfig` and add the variant output configuration:

```kotlin
defaultConfig {
    versionCode = 2
    versionName = "1.1.0"
}

androidComponents {
    onVariants(selector().all()) { variant ->
        variant.outputs.forEach { output ->
            output.outputFileName.set(
                "BookBox-${variant.versionName.get()}-${variant.versionCode.get()}-${variant.buildType}.apk",
            )
        }
    }
}
```

- [ ] **Step 3: Run the configuration assertion**

Run the PowerShell assertion from Step 1.

Expected: PASS with exit code 0.

- [ ] **Step 4: Build the debug APK with JDK 17**

```powershell
$env:JAVA_HOME = 'C:\Program Files\Microsoft\jdk-17.0.20.101-hotspot'
$env:Path = (Join-Path $env:JAVA_HOME 'bin') + ';' + $env:Path
Set-Location android
.\gradlew.bat :app:assembleDebug --no-daemon
```

Expected: `BUILD SUCCESSFUL`.

- [ ] **Step 5: Verify the artifact**

```powershell
$apk = 'android/app/build/outputs/apk/debug/BookBox-1.1.0-2-debug.apk'
if (-not (Test-Path -LiteralPath $apk)) { throw "Missing $apk" }
Get-Item -LiteralPath $apk | Select-Object FullName, Length, LastWriteTime
Get-FileHash -LiteralPath $apk -Algorithm SHA256
```

Expected: the versioned APK exists with a nonzero size and a SHA-256 checksum.

- [ ] **Step 6: Commit**

```powershell
git add -- android/app/build.gradle.kts docs/superpowers/plans/2026-09-01-apk-versioning.md
git commit -m "build: version Android APK artifacts"
```
