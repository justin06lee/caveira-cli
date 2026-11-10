import { lazy, memo, Suspense } from "react";

// react-markdown, remark-gfm and highlight.js are most of the bundle, so they load on their own after the window
// draws. Until they arrive a reply shows as plain text.
export const loadMarkdown = () => import("./MarkdownBody");
const Body = lazy(loadMarkdown);

export const Markdown = memo(function Markdown({ text }: { text: string }) {
  return (
    <Suspense
      fallback={
        <div className="prose">
          <p className="plain">{text}</p>
        </div>
      }
    >
      <Body text={text} />
    </Suspense>
  );
});
