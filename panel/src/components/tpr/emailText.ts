// Domain labels stop before ".json" so credential file names keep their extension visible.
const EMAIL_PATTERN = /[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.(?!json\b)[A-Za-z0-9-]+)+/g;

export const splitEmails = (text: string): Array<{ text: string; email: boolean }> => {
  const parts: Array<{ text: string; email: boolean }> = [];
  let last = 0;
  for (const match of text.matchAll(EMAIL_PATTERN)) {
    const start = match.index ?? 0;
    if (start > last) parts.push({ text: text.slice(last, start), email: false });
    parts.push({ text: match[0], email: true });
    last = start + match[0].length;
  }
  if (last < text.length) parts.push({ text: text.slice(last), email: false });
  return parts;
};

export const maskEmails = (text: string): string => text.replace(EMAIL_PATTERN, '•••@•••');
