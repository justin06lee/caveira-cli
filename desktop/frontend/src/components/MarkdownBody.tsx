import { memo, useRef, useState, type ReactNode } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeHighlight from "rehype-highlight";
import { Check, Copy } from "lucide-react";
import { copyText, openURL } from "../lib/bridge";

function CodeBlock({ children }: { children?: ReactNode }) {
  const ref = useRef<HTMLPreElement>(null);
  const [copied, setCopied] = useState(false);
  const code = Array.isArray(children) ? children[0] : children;
  const cls = (code as { props?: { className?: string } } | undefined)?.props?.className ?? "";
  const lang = /language-([\w+#-]+)/.exec(cls)?.[1] ?? "";

  return (
    <div className="code">
      <div className="code-head">
        <span>{lang || "code"}</span>
        <button
          className="copy-btn"
          onClick={async () => {
            await copyText(ref.current?.innerText.replace(/\n$/, "") ?? "");
            setCopied(true);
            window.setTimeout(() => setCopied(false), 1600);
          }}
        >
          {copied ? <Check size={12} /> : <Copy size={12} />}
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <pre ref={ref}>{children}</pre>
    </div>
  );
}

const components: Components = {
  pre: ({ children }) => <CodeBlock>{children}</CodeBlock>,
  a: ({ href, children }) => (
    <a
      href={href}
      onClick={(e) => {
        e.preventDefault();
        if (href && /^https?:\/\//.test(href)) openURL(href);
      }}
    >
      {children}
    </a>
  ),
};

// Loaded on its own (see Markdown.tsx): the markdown and highlighting libraries are most of the bundle.
export default memo(function MarkdownBody({ text }: { text: string }) {
  return (
    <div className="prose">
      <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeHighlight]} components={components}>
        {text}
      </ReactMarkdown>
    </div>
  );
});
