import { useEffect, useRef, useState, type ChangeEvent, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api, errorMessage, formatBytes } from "../api";
import { Layout, PageHeading } from "../components/Layout";
import type { Asset, Block, ItemInput, Share, User, UserConfig } from "../types";

type Align = "left" | "center" | "right";
type CellDraft = { text: string; align: Align };
type RowDraft = { cells: CellDraft[]; assets: Asset[] };

function blankRow(columns: number, align: Align): RowDraft {
  return { cells: Array.from({ length: columns }, () => ({ text: "", align })), assets: [] };
}

export function CreateSharePage({ user, onLogout }: { user: User; onLogout: () => void }) {
  const navigate = useNavigate();
  const [config, setConfig] = useState<UserConfig | null>(null);
  const [title, setTitle] = useState("");
  const [sessionMinutes, setSessionMinutes] = useState(15);
  const [validityHours, setValidityHours] = useState(24);
  const [defaultAlign, setDefaultAlign] = useState<Align>("center");
  const [ipAllow, setIPAllow] = useState("");
  const [ipDeny, setIPDeny] = useState("");
  const [rows, setRows] = useState<RowDraft[]>([blankRow(1, "center")]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [uploadingRow, setUploadingRow] = useState<number | null>(null);
  const [created, setCreated] = useState<Share | null>(null);

  useEffect(() => {
    api.config().then(({ config: value }) => {
      setConfig(value);
      setSessionMinutes(Math.round(value.defaultSessionTimeoutSeconds / 60));
      setValidityHours(Math.round(value.defaultLinkValiditySeconds / 3600));
    }).catch((err) => setError(errorMessage(err)));
  }, []);

  function setCell(rowIndex: number, columnIndex: number, patch: Partial<CellDraft>) {
    setRows((current) => current.map((row, r) => r === rowIndex
      ? { ...row, cells: row.cells.map((cell, c) => c === columnIndex ? { ...cell, ...patch } : cell) }
      : row));
  }

  function addRow() {
    const columns = rows[0]?.cells.length || 1;
    setRows((current) => [...current, blankRow(columns, defaultAlign)]);
  }

  function addColumn() {
    if (!config) return;
    if ((rows[0]?.cells.length || 0) >= config.maxColumns) {
      setError(`最多允许 ${config.maxColumns} 列`);
      return;
    }
    setRows((current) => current.map((row) => ({ ...row, cells: [...row.cells, { text: "", align: defaultAlign }] })));
  }

  function removeColumn() {
    if ((rows[0]?.cells.length || 0) <= 1) return;
    setRows((current) => current.map((row) => ({ ...row, cells: row.cells.slice(0, -1) })));
  }

  function removeRow(index: number) {
    if (rows.length <= 1) return;
    setRows((current) => current.filter((_, i) => i !== index));
  }

  async function importExcel(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file || !config) return;
    setError("");
    try {
      if (file.size > config.maxFileBytes) throw new Error("Excel 文件超过上传大小上限");
      const ExcelJS = await import("exceljs");
      const workbook = new ExcelJS.Workbook();
      await workbook.xlsx.load(await file.arrayBuffer());
      const sheet = workbook.worksheets[0];
      if (!sheet) throw new Error("工作簿中没有工作表");
      const imported: string[][] = [];
      sheet.eachRow({ includeEmpty: false }, (row) => {
        const values: string[] = [];
        const max = Math.min(row.cellCount, config.maxColumns);
        for (let column = 1; column <= max; column++) {
          values.push(row.getCell(column).text.trim());
        }
        if (values.some(Boolean)) imported.push(values);
      });
      if (imported.length === 0) throw new Error("第一个工作表没有可导入的文本");
      if (imported.length > config.maxItemsPerShare) throw new Error(`最多导入 ${config.maxItemsPerShare} 行`);
      const columns = Math.min(Math.max(1, ...imported.map((row) => row.length)), config.maxColumns);
      setRows(imported.map((values) => ({
        cells: Array.from({ length: columns }, (_, i) => ({ text: values[i] || "", align: defaultAlign })),
        assets: []
      })));
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  async function uploadFiles(rowIndex: number, event: ChangeEvent<HTMLInputElement>) {
    const files = Array.from(event.target.files || []);
    event.target.value = "";
    if (!files.length || !config) return;
    setUploadingRow(rowIndex);
    setError("");
    try {
      const uploaded: Asset[] = [];
      for (const file of files) {
        if (file.size > config.maxFileBytes) throw new Error(`${file.name} 超过 ${formatBytes(config.maxFileBytes)}`);
        uploaded.push(await api.uploadAsset(file));
      }
      setRows((current) => current.map((row, i) => i === rowIndex ? { ...row, assets: [...row.assets, ...uploaded] } : row));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setUploadingRow(null);
    }
  }

  function removeAsset(rowIndex: number, assetId: string) {
    setRows((current) => current.map((row, i) => i === rowIndex
      ? { ...row, assets: row.assets.filter((asset) => asset.id !== assetId) }
      : row));
  }

  function buildItems(): ItemInput[] {
    return rows.map((row, index) => {
      const blocks: Block[] = row.cells
        .filter((cell) => cell.text.trim())
        .map((cell) => ({ type: "markdown", text: cell.text.trim(), align: cell.align }));
      for (const asset of row.assets) {
        blocks.push({
          type: asset.mimeType.startsWith("image/") ? "image" : "file",
          assetId: asset.id,
          assetName: asset.name,
          mimeType: asset.mimeType,
          size: asset.size
        });
      }
      if (!blocks.length) throw new Error(`第 ${index + 1} 行没有任何内容`);
      return { blocks };
    });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!config) return;
    setError("");
    setBusy(true);
    try {
      const sessionSeconds = Math.round(sessionMinutes * 60);
      const validitySeconds = Math.round(validityHours * 3600);
      if (sessionSeconds > config.maxSessionTimeoutSeconds) throw new Error("会话时长超过管理员配置上限");
      if (validitySeconds > config.maxLinkValiditySeconds) throw new Error("链接有效期超过管理员配置上限");
      if (rows.length > config.maxItemsPerShare) throw new Error("内容行数超过管理员配置上限");
      const result = await api.createShare({
        title,
        sessionTimeoutSeconds: sessionSeconds,
        linkValiditySeconds: validitySeconds,
        ipAllow: splitCIDRs(ipAllow),
        ipDeny: splitCIDRs(ipDeny),
        defaultAlign,
        items: buildItems()
      });
      setCreated(result.share);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  if (created) {
    return (
      <Layout user={user} onLogout={onLogout}>
        <section className="created-card">
          <div className="success-orb">✓</div>
          <div className="eyebrow">LINK READY</div>
          <h1>分享链接已创建</h1>
          <p>链接密钥位于 URL 的 <code>#k=</code> 片段中，不进入服务器访问日志。请通过安全渠道发送给接收者。</p>
          <div className="secret-link">{created.publicLink}</div>
          <div className="button-row centered">
            <button className="button primary" onClick={() => void navigator.clipboard.writeText(created.publicLink || "")}>复制安全链接</button>
            <button className="button secondary" onClick={() => navigate("/")}>返回控制台</button>
          </div>
          <p className="fine-print">关闭页面不等于文件已从接收设备消失；接收者仍可截图、复制或保存已下载附件。</p>
        </section>
      </Layout>
    );
  }

  const columns = rows[0]?.cells.length || 1;
  return (
    <Layout user={user} onLogout={onLogout} wide>
      <PageHeading
        eyebrow="NEW DROP"
        title="创建分享链接"
        description="每一行是一份独立内容。新标签页领取下一行；刷新当前标签页不会重新分配。"
        actions={<Link className="button ghost" to="/">取消</Link>}
      />
      <form onSubmit={submit} className="create-form">
        <section className="panel form-section">
          <div className="section-number">01</div>
          <div className="section-content">
            <h2>基本设置</h2>
            <div className="form-grid two">
              <label className="span-two">分享标题<input value={title} onChange={(e) => setTitle(e.target.value)} maxLength={100} placeholder="例如：周五兑换码" required /></label>
              <label>单会话最长展示时间<input type="number" min={0.5} max={(config?.maxSessionTimeoutSeconds || 3600) / 60} step={0.5} value={sessionMinutes} onChange={(e) => setSessionMinutes(Number(e.target.value))} required /><small>从内容成功展示开始计时，刷新不重置。</small></label>
              <label>链接有效期（小时）<input type="number" min={1} max={(config?.maxLinkValiditySeconds || 86400) / 3600} step={0.5} value={validityHours} onChange={(e) => setValidityHours(Number(e.target.value))} required /><small>到期后未领取和活动中的内容一并销毁。</small></label>
              <label>默认对齐方式<select value={defaultAlign} onChange={(e) => { const align = e.target.value as Align; setDefaultAlign(align); setRows((current) => current.map((row) => ({ ...row, cells: row.cells.map((cell) => ({ ...cell, align })) }))); }}><option value="center">居中</option><option value="left">左对齐</option><option value="right">右对齐</option></select></label>
              <div className="config-hint"><span>允许附件</span><strong>{config?.allowedExtensions.join(" · ") || "读取中…"}</strong><span>单文件至 {formatBytes(config?.maxFileBytes || 0)}</span></div>
            </div>
          </div>
        </section>

        <section className="panel form-section">
          <div className="section-number">02</div>
          <div className="section-content">
            <div className="section-title-line">
              <div><h2>内容表格</h2><p>单元格支持 Markdown；空白单元格会被忽略。</p></div>
              <label className="button secondary small file-button">导入 Excel<input type="file" accept=".xlsx" onChange={(e) => void importExcel(e)} /></label>
            </div>
            <div className="table-toolbar">
              <span>{rows.length} 行 · {columns} 列</span>
              <div className="button-row">
                <button type="button" className="button ghost small" onClick={addRow}>＋ 行</button>
                <button type="button" className="button ghost small" onClick={addColumn}>＋ 列</button>
                <button type="button" className="button ghost small" onClick={removeColumn}>－ 列</button>
              </div>
            </div>
            <div className="editor-scroll">
              <table className="content-editor">
                <thead><tr><th>#</th>{Array.from({ length: columns }, (_, i) => <th key={i}>列 {i + 1}</th>)}<th>附件</th><th /></tr></thead>
                <tbody>
                  {rows.map((row, rowIndex) => (
                    <tr key={rowIndex}>
                      <th>{rowIndex + 1}</th>
                      {row.cells.map((cell, columnIndex) => (
                        <td key={columnIndex}>
                          <div className="cell-tools">
                            <select aria-label="对齐方式" value={cell.align} onChange={(e) => setCell(rowIndex, columnIndex, { align: e.target.value as Align })}><option value="center">中</option><option value="left">左</option><option value="right">右</option></select>
                          </div>
                          <textarea value={cell.text} onChange={(e) => setCell(rowIndex, columnIndex, { text: e.target.value })} placeholder="输入 Markdown…" rows={5} />
                        </td>
                      ))}
                      <td className="attachment-cell">
                        {row.assets.map((asset) => (
                          <div className="attachment-chip" key={asset.id}><span>{asset.mimeType.startsWith("image/") ? "▧" : "↧"} {asset.name}</span><button type="button" onClick={() => removeAsset(rowIndex, asset.id)}>×</button></div>
                        ))}
                        <label className="add-file">{uploadingRow === rowIndex ? "上传中…" : "＋ 图片 / 文件"}<input type="file" multiple accept={(config?.allowedExtensions || []).map((ext) => `.${ext}`).join(",")} onChange={(e) => void uploadFiles(rowIndex, e)} disabled={uploadingRow !== null} /></label>
                      </td>
                      <td><button type="button" className="icon-button" title="删除此行" onClick={() => removeRow(rowIndex)} disabled={rows.length <= 1}>×</button></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </section>

        <section className="panel form-section">
          <div className="section-number">03</div>
          <div className="section-content">
            <h2>网络限制</h2>
            <p>每行一个 IPv4、IPv6 地址或 CIDR。白名单为空表示允许所有网络；黑名单始终优先。</p>
            <div className="form-grid two">
              <label>允许的 CIDR<textarea rows={4} value={ipAllow} onChange={(e) => setIPAllow(e.target.value)} placeholder="203.0.113.0/24" /></label>
              <label>禁止的 CIDR<textarea rows={4} value={ipDeny} onChange={(e) => setIPDeny(e.target.value)} placeholder="198.51.100.42/32" /></label>
            </div>
          </div>
        </section>
        {error && <div className="alert error">{error}</div>}
        <div className="submit-bar">
          <div><strong>{rows.length} 份内容</strong><span>提交后，链接密钥只在此创建流程中展示一次</span></div>
          <button className="button primary large" disabled={busy || !config || uploadingRow !== null}>{busy ? "正在加密并创建…" : "创建分享链接"}</button>
        </div>
      </form>
    </Layout>
  );
}

function splitCIDRs(value: string): string[] {
  return value.split(/[\n,]+/).map((item) => item.trim()).filter(Boolean);
}

