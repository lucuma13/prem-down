# prem-down

![OS](https://img.shields.io/badge/OS-macOS%20%7C%20Windows-lightgrey)
[![CI](https://github.com/lucuma13/prem-down/actions/workflows/ci.yml/badge.svg)](https://github.com/lucuma13/prem-down/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/lucuma13/prem-down/graph/badge.svg?token=GU64739473)](https://codecov.io/gh/lucuma13/prem-down)

`prem-down` downgrades an Adobe Premiere Pro project file (`.prproj`) so an older version of Premiere can open it.

It fully supports the breaking changes introduced with *Premiere Pro 2026*. The well-known method (gunzip the `.prproj`, lower the top-level `<Project>` `Version`, re-gzip) no longer works reliably on Premiere 2026 files. The cause is that 2026 uses sparser serialisation — it drops default-valued fields — but some classes (e.g. transitions, effect parameters) require those fields to be present in older releases.

So the fix is bifold:
- re-insert the dropped fields with their default values
- set the `<Project>` version to the target release.

### 🚀 Installation

Download and execute the binary from the [releases page](https://github.com/Lucuma13/prem-down/releases)


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
