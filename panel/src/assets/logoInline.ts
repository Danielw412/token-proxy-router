/** Token Proxy Router mark: two routes merging into one, as an inline SVG data URL. */
const BRAND_LOGO_SVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="7" fill="#1a1a1a"/><path d="M6 10.5h7.5c3 0 4.2 5.5 7 5.5H26M6 21.5h7.5c3 0 4.2-5.5 7-5.5" fill="none" stroke="#f4f4f4" stroke-width="2.4" stroke-linecap="round"/><circle cx="25.5" cy="16" r="2.6" fill="#e8876a"/></svg>`;

export const BRAND_LOGO = `data:image/svg+xml,${encodeURIComponent(BRAND_LOGO_SVG)}`;

export const BRAND_NAME = 'Token Proxy Router';
