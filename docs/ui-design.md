# Native settings navigation

The interface combines Fluent 2 styling with a two-level settings hierarchy informed by Apple's Human Interface Guidelines. All controls and navigation use native MyGo components.

## Navigation

The initial settings view contains full-width cards in this order: Video, Audio, Recording, Presets, App settings, and About. Each card has an outline icon, title, short description, and disclosure chevron. The first five cards open a settings page. App settings contains language selection and the persistent debug-mode switch; the overview does not display the language selector. App settings follows Presets and appears immediately above About. About opens a modal with app and developer information and searchable dependency notices. Selecting a license opens a second modal with the full text. Closing the license returns to About; closing About returns to the settings overview. The capture window opens from the toolbar. A leading back button returns from a settings page without changing capture settings or interrupting video, monitoring, or recording. The preview stays visible beside the settings panel in wide windows.

The settings panel is 330 logical pixels wide. At widths of at least 1120 logical pixels, quick controls share the command bar: a 200-pixel preset selector and 128-pixel Record and Start/Stop buttons use 32-pixel control heights. Medium windows align the preset selector with the settings column. Below 560 pixels, the full-width selector sits above the Record/Start action row. Compact windows expose the same hierarchy inside the existing Settings dialog. Overview cards and detail controls scroll vertically within their own bounds. Category labels do not require a horizontal tab bar or reduced text size.

## Visual rules

| Element | Application rule |
| --- | --- |
| Colors and controls | Existing Fluent light/dark palette, blue accent, native select/input/button controls |
| Main settings title | 20 logical pixels, semibold |
| Detail navigation title | 16 logical pixels, semibold |
| Card title and description | 14 and 12 logical pixels |
| Card geometry | 76-pixel height, 12-pixel internal padding, 8-pixel gaps, 6-pixel corner radius |
| Icons | Consistent blue outline icons drawn locally; system-font disclosure glyphs |
| Alignment | Leading-aligned text, centered icons and disclosure indicators |
| Frequent actions | With saved presets, the command bar orders the preset selector, Record, Start/Stop video, and the adjacent Capture window/Fullscreen group. Narrow windows reflow controls into bounded rows |

These values are application design choices. They do not claim to reproduce an Apple platform component or prescribe Apple-specific control dimensions on Linux.

## Rationale and sources

[Fluent layout](https://fluent2.microsoft.design/layout) defines a four-pixel spacing base and explains how proximity and alignment communicate relationships. [Fluent cards](https://fluent2.microsoft.design/components/web/react/core/card/usage) supports using the full card as one clear navigation action with concise content.

[Apple HIG layout](https://developer.apple.com/design/human-interface-guidelines/layout) recommends a clear visual hierarchy, consistent alignment, related groups, and layouts that adapt to window and text sizes. [Apple HIG settings](https://developer.apple.com/design/human-interface-guidelines/settings) recommends discoverable settings, sensible defaults, respect for system preferences, and keeping task actions available where they are used. The overview/detail hierarchy is this application's adaptation of those principles. The application initially follows system appearance. The native sun/moon theme toggle selects light or dark appearance for the current session. Fluent controls remain in use.

## Verification

Headless native-UI tests cover overview-to-detail navigation, return navigation, preserved capture choices, compact scrolling, and panel boundaries from 320 × 240 through 1920 × 1080. Video fitting and fullscreen tests remain applicable to the unchanged preview layout. Changes generated with coding assistance require human review before release.
