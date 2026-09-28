// Aurora theme engine — opencode-compatible (MIT).
// Resolves design tokens for the selected theme + colour scheme and injects
// them as CSS custom properties on :root.
//
// Copied to internal/web/dist/theme/index.js by scripts/build-web-themes.mjs.
//
// Storage mirrors opencode, with an `aurora-` prefix:
//   aurora-theme-id       theme id          (default "oc-2")
//   aurora-color-scheme   system|light|dark (default "system")
//   aurora-theme-css-*    cached token sheet per scheme (skipped for oc-2)
//
// The static fallback lives in internal/web/dist/theme.css and is generated
// from the same resolver, so the first paint never flashes a different palette.

import { THEMES } from "./themes-data.js"
import { resolveThemeVariantV2 } from "./resolve.js"

const DEFAULT_THEME = "oc-2"
const KEYS = {
  themeId: "aurora-theme-id",
  scheme: "aurora-color-scheme",
  css: (mode) => `aurora-theme-css-${mode}`,
}
const STYLE_ID = "aurora-theme"
const PRELOAD_ID = "aurora-theme-preload"
const SYSTEM_BG = { dark: "#080808", light: "#fafafa" }

const schemeQuery = window.matchMedia("(prefers-color-scheme: dark)")

function read(key, fallback) {
  try {
    const value = localStorage.getItem(key)
    return value === null ? fallback : value
  } catch {
    return fallback
  }
}

function write(key, value) {
  try {
    localStorage.setItem(key, value)
  } catch {}
}

function themeById(id) {
  return THEMES.find((theme) => theme.id === id) || THEMES.find((theme) => theme.id === DEFAULT_THEME)
}

export function resolveTokens(theme, mode) {
  const dark = mode === "dark"
  return resolveThemeVariantV2(dark ? theme.dark : theme.light, dark)
}

// One level of var() indirection, so swatches can show a literal colour.
function deref(tokens, key) {
  let value = tokens[key]
  for (let i = 0; i < 4; i++) {
    const match = typeof value === "string" && value.match(/^var\(--(.+)\)$/)
    if (!match) break
    value = tokens[match[1]]
  }
  return value || "transparent"
}

export function tokensCss(theme, mode) {
  const lines = Object.entries(resolveTokens(theme, mode)).map(([key, value]) => `--${key}:${value};`)
  return `:root{color-scheme:${mode};${lines.join("")}}`
}

function schemeMode(scheme) {
  return scheme === "system" ? (schemeQuery.matches ? "dark" : "light") : scheme
}

function readState() {
  return {
    themeId: read(KEYS.themeId, DEFAULT_THEME),
    scheme: read(KEYS.scheme, "system"),
  }
}

function sheetElement() {
  let sheet = document.getElementById(STYLE_ID)
  if (!sheet) {
    sheet = document.createElement("style")
    sheet.id = STYLE_ID
    document.head.appendChild(sheet)
  }
  return sheet
}

function apply(state, { persist = true } = {}) {
  const theme = themeById(state.themeId)
  const mode = schemeMode(state.scheme)

  document.getElementById(PRELOAD_ID)?.remove()

  if (theme.id === DEFAULT_THEME) {
    try {
      localStorage.removeItem(KEYS.css("dark"))
      localStorage.removeItem(KEYS.css("light"))
    } catch {}
    sheetElement().textContent = ""
  } else {
    const css = tokensCss(theme, mode)
    write(KEYS.css(mode), css)
    write(KEYS.css(mode === "dark" ? "light" : "dark"), tokensCss(theme, mode === "dark" ? "light" : "dark"))
    sheetElement().textContent = css
  }

  document.documentElement.dataset.theme = theme.id
  document.documentElement.dataset.colorScheme = mode
  document.documentElement.style.backgroundColor = SYSTEM_BG[mode]

  const themeColor = document.querySelector('meta[name="theme-color"]')
  if (themeColor) themeColor.setAttribute("content", SYSTEM_BG[mode])

  if (persist) {
    write(KEYS.themeId, theme.id)
    write(KEYS.scheme, state.scheme)
  }

  const detail = { themeId: theme.id, scheme: state.scheme, mode }
  window.dispatchEvent(new CustomEvent("aurora:theme-change", { detail }))
  return detail
}

let state = readState()

const api = {
  themes: THEMES,
  current: () => ({ ...state, mode: schemeMode(state.scheme) }),
  setTheme(themeId) {
    state = { ...state, themeId }
    return apply(state)
  },
  setScheme(scheme) {
    state = { ...state, scheme }
    return apply(state)
  },
  // Surface colours of a theme in both schemes (used for picker swatches).
  sample(themeId) {
    const theme = themeById(themeId)
    return {
      light: deref(resolveTokens(theme, "light"), "v2-background-bg-base"),
      dark: deref(resolveTokens(theme, "dark"), "v2-background-bg-base"),
    }
  },
  // Re-apply from storage (used by the preload path and cross-tab sync).
  sync() {
    state = readState()
    return apply(state, { persist: false })
  },
}

window.AuroraTheme = api

// First paint: the preload script already set the default-oc-2 tokens for
// other themes; this pass validates state and settles the sheet.
apply(state, { persist: false })

schemeQuery.addEventListener("change", () => {
  if (state.scheme === "system") apply(state, { persist: false })
})

window.addEventListener("storage", (event) => {
  if (event.key === KEYS.themeId || event.key === KEYS.scheme) api.sync()
})

window.dispatchEvent(new CustomEvent("aurora:theme-ready", { detail: { ...api.current() } }))
