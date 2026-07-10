# prem-down

![OS](https://img.shields.io/badge/OS-macOS%20%7C%20Windows-lightgrey)
[![CI](https://github.com/lucuma13/prem-down/actions/workflows/ci.yml/badge.svg)](https://github.com/lucuma13/prem-down/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/lucuma13/prem-down/graph/badge.svg?token=GU64739473)](https://codecov.io/gh/lucuma13/prem-down)

`prem-down` downgrades an Adobe Premiere Pro project file so an older version of Premiere can open it.

Operation runs completely **offline and local** to your machine – no data is ever uploaded to the internet.

It fully supports the breaking changes introduced with **Premiere Pro 2026**. The well-known method (gunzip the `.prproj`, lower the top-level project version, re-gzip) no longer works reliably on Premiere 2026 files. The cause is that 2026 uses sparser serialisation — it drops fields that older releases expect present (and report the project as damaged if they are absent). So the fix is bifold: re-insert those required fields, and set the project version to the target release.


### 🚀 Installation

#### macOS

1. Install [Homebrew](https://brew.sh/) (if not already installed):
```sh
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

2. Tap and install:
```sh
brew tap lucuma13/dit
brew install prem-down
```

#### Windows

Download the installer from [here](https://github.com/lucuma13/prem-down/releases/latest) and open it (Windows will warn about an unknown publisher, choose "More info" > "Run anyway"). Alternatively, install from Terminal:

```sh
winget install -e --id lucuma13.prem-down
```

### 📖 Usage

The tool creates a downgraded copy of the project file, leaving the original untouched.

#### Context Menu Integration

Downgrade any project to the previous release:

* macOS Finder: right-click any `.prproj` file and choose "Quick Actions > Downgrade for older Premiere"
* Windows File Explorer: right-click any `.prproj` file and choose "Downgrade for older Premiere"

#### Command Line Interface

Downgrade any project to the previous release:
```
prem-down myproject.prproj
````

Downgrade any project to specific Premiere Pro version:
```sh
prem-down myproject.prproj --to 2024
```

If the action is ever missing (or you want to remove it before uninstalling), manage it from the Terminal:

```sh
prem-down integrate           # add the right-click action
prem-down integrate --remove  # remove it
```


### 🧪 Feedback & Contributing

While backwards-compatibility between Premiere Pro 2026 and 2025 has been throrougly tested, you must verify your downgraded project manually. Features, effects or tools native to newer Premiere releases will not render or translate when opened in an older version.

#### Found a bug?
If a downgraded project fails to load or exhibits unexpected behavior, please submit a detailed issue on the GitHub repository with the source and target version numbers.