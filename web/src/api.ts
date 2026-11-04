function cookieValue(name: string): string {
  const prefix = `${encodeURIComponent(name)}=`;
  const item = document.cookie.split('; ').find((part) => part.startsWith(prefix));
  return item ? decodeURIComponent(item.slice(prefix.length)) : '';
}

export function secureFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  // On https the server names it __Host-ky_csrf (a sibling host cannot plant it).
  const csrf = cookieValue('__Host-ky_csrf') || cookieValue('ky_csrf');
  if (csrf) headers.set('X-CSRF-Token', csrf);
  return fetch(input, { ...init, headers });
}
