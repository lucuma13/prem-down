# prem-down

![OS](https://img.shields.io/badge/OS-macOS%20%7C%20Windows-lightgrey)
[![CI](https://github.com/lucuma13/prem-down/actions/workflows/ci.yml/badge.svg)](https://github.com/lucuma13/prem-down/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/lucuma13/prem-down/graph/badge.svg?token=GU64739473)](https://codecov.io/gh/lucuma13/prem-down)

`prem-down` downgrades an Adobe Premiere Pro project file so an older version of Premiere can open it.

It fully supports the breaking changes introduced with Premiere Pro 2026. The well-known method (gunzip the `.prproj`, lower the top-level project version, re-gzip) no longer works reliably on Premiere 2026 files. The cause is that 2026 uses sparser serialisation — it drops fields that older releases expect present (and report the project as damaged if they are absent).

So the fix is bifold:
- re-insert those required fields
- set the project version to the target release


### 🚀 Installation

##### macOS

1. Install [Homebrew](https://brew.sh/) (if not already installed):
```
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

2. Tap and install:
```
brew tap lucuma13/dit
brew install prem-down
```

##### Windows

1. Install:

```
winget install -e --id lucuma13.prem-down
```


### 📖 Usage examples

Downgrade any project to the previous release:

```sh
prem-down myproject.prproj
```

Downgrade any project to Premiere Pro 2024:
```sh
prem-down myproject.prproj --to 2024
```

Known releases: `2026`, `2025`, `2024`, `2023`, `2022`, `2021`, `2020.1`, `2020`, `2019.1`, `2019`, `2018.1`, `2018`, `2017`, `2015.1`, `2015`, `2014.1`, `2014`, `CC`, `CS6`, `CS5.5`, `CS5`, `CS4` (CC-era aliases like `CC2019` also work).
