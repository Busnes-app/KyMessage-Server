import MarkdownIt from 'markdown-it';

// Keep raw HTML, plugins and custom highlighters disabled. The parser escapes
// source HTML and validates links before producing the only HTML sink we use.
const markdown = new MarkdownIt({html:false,breaks:true,linkify:false,typographer:false});
markdown.disable('image'); // Never fetch a sender-controlled image on message display.
markdown.validateLink = href => {
  try { return ['https:','http:'].includes(new URL(href).protocol); }
  catch { return false; }
};

export function messageBody(source: string): HTMLElement {
  const node = document.createElement('div');
  node.className = 'message-body';
  node.innerHTML = markdown.render(source);
  for (const link of node.querySelectorAll('a')) {
    link.rel = 'noopener noreferrer';
    link.target = '_blank';
  }
  return node;
}
