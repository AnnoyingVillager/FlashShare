export function LoadingScreen({ label }: { label: string }) {
  return (
    <div className="full-center">
      <div className="loading-mark" aria-hidden="true"><span /><span /><span /></div>
      <p>{label}</p>
    </div>
  );
}

