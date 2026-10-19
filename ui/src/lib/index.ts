// @nulldrive/ui — the shared component library.
//
// Used by the web UI (src/web) and, from Phase 4, the Wails desktop client.
// Rules for anything exported from here:
//   - No routing, no window.location, no global fetch: components take data
//     via props and report user intent via callback props (onopen, onaction).
//   - No server URL assumptions: the API client takes a baseUrl and token.
//   - Styling only via the CSS variables in styles/tokens.css.

export { default as Breadcrumbs } from './components/Breadcrumbs.svelte';
export { default as Button } from './components/Button.svelte';
export { default as Card } from './components/Card.svelte';
export { default as EmptyState } from './components/EmptyState.svelte';
export { default as FileIcon } from './components/FileIcon.svelte';
export { default as FileRow, type FileItem } from './components/FileRow.svelte';
export { default as FileTile } from './components/FileTile.svelte';
export { default as Icon, type IconName, iconNames } from './components/Icon.svelte';
export { default as Input } from './components/Input.svelte';
export { default as Logo } from './components/Logo.svelte';
export { default as Menu, type MenuItem } from './components/Menu.svelte';
export { default as Modal } from './components/Modal.svelte';
export { default as PasswordStrength } from './components/PasswordStrength.svelte';
export { default as ProgressBar } from './components/ProgressBar.svelte';
export { default as Sidebar, type NavItem } from './components/Sidebar.svelte';
export { default as Toast } from './components/Toast.svelte';
export { default as ToastHost } from './components/ToastHost.svelte';

export { toast, type ToastKind } from './toast.svelte';
export { getTheme, setTheme, getScanlines, setScanlines, type Theme } from './theme';
export { NullDriveClient, ApiError, encodePath, type ClientOptions, type UploadOptions } from './api/client';
export type * from './api/types';
export * from './util/format';
export * from './util/names';
export * from './util/paths';
export * from './util/strength';
