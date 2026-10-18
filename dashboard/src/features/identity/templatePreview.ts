// The template preview: Firebase's %VARIABLES% filled with sample values, and an HTML body
// reduced to its text. Nothing from the template is ever inserted as markup.

export const TEMPLATE_VARIABLES = ['%APP_NAME%', '%DISPLAY_NAME%', '%EMAIL%', '%NEW_EMAIL%', '%LINK%'] as const;

export type TemplateVariable = (typeof TEMPLATE_VARIABLES)[number];

export function fillVariables(text: string, values: Record<TemplateVariable, string>): string {
  return text.replace(/%(APP_NAME|DISPLAY_NAME|EMAIL|NEW_EMAIL|LINK)%/g, (match) => values[match as TemplateVariable]);
}

/** The text of an HTML body, with paragraphs and line breaks kept as new lines. */
export function bodyText(body: string): string {
  if (!/<[a-z!/]/i.test(body)) return body;
  const marked = body.replace(/<br\s*\/?>/gi, '\n').replace(/<\/(p|div|h[1-6]|li|tr)>/gi, '\n');
  // DOMParser builds an inert document: scripts do not run and nothing loads.
  const doc = new DOMParser().parseFromString(marked, 'text/html');
  for (const el of doc.querySelectorAll('script, style, title')) el.remove();
  return (doc.body.textContent ?? '').replace(/\n{3,}/g, '\n\n').trim();
}
