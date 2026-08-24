package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const maxOfficeContentRunes = 50000

type officeSlide struct {
	Title string
	Body  []string
}

func escapeOfficeXML(value string) string {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(value))
	return output.String()
}

func buildOfficeArchive(files map[string]string) ([]byte, error) {
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		part, err := archive.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err = part.Write([]byte(files[name])); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func docxParagraph(text string, title bool) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return `<w:p/>`
	}
	runProperties := ""
	if title {
		runProperties = `<w:rPr><w:b/><w:sz w:val="32"/></w:rPr>`
	}
	return `<w:p><w:r>` + runProperties + `<w:t xml:space="preserve">` + escapeOfficeXML(text) + `</w:t></w:r></w:p>`
}

func createDOCX(title, content string) ([]byte, error) {
	paragraphs := []string{docxParagraph(title, true)}
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		heading := strings.HasPrefix(strings.TrimSpace(line), "#")
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		paragraphs = append(paragraphs, docxParagraph(line, heading))
	}
	return buildOfficeArchive(map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
			strings.Join(paragraphs, "") + `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr></w:body></w:document>`,
	})
}

func spreadsheetRows(content string) ([][]string, error) {
	reader := csv.NewReader(strings.NewReader(strings.TrimSpace(content)))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, errors.New("spreadsheet content must be valid CSV text")
	}
	if len(rows) == 0 || len(rows) > 500 {
		return nil, errors.New("spreadsheet must contain 1 to 500 rows")
	}
	for _, row := range rows {
		if len(row) > 50 {
			return nil, errors.New("spreadsheet must not exceed 50 columns")
		}
	}
	return rows, nil
}

func spreadsheetColumn(index int) string {
	value := ""
	for index >= 0 {
		value = string(rune('A'+index%26)) + value
		index = index/26 - 1
	}
	return value
}

func createXLSX(title, content string) ([]byte, error) {
	rows, err := spreadsheetRows(content)
	if err != nil {
		return nil, err
	}
	var sheet strings.Builder
	for rowIndex, row := range rows {
		sheet.WriteString(fmt.Sprintf(`<row r="%d">`, rowIndex+1))
		for columnIndex, cell := range row {
			reference := spreadsheetColumn(columnIndex) + fmt.Sprint(rowIndex+1)
			sheet.WriteString(`<c r="` + reference + `" t="inlineStr"><is><t xml:space="preserve">` + escapeOfficeXML(cell) + `</t></is></c>`)
		}
		sheet.WriteString(`</row>`)
	}
	return buildOfficeArchive(map[string]string{
		"[Content_Types].xml":        `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`,
		"_rels/.rels":                `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
		"xl/workbook.xml":            `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="` + escapeOfficeXML(title) + `" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` + sheet.String() + `</sheetData></worksheet>`,
	})
}

func parseOfficeSlides(title, content string) []officeSlide {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	blocks := strings.Split(content, "\n---\n")
	if len(blocks) == 1 {
		blocks = nil
		var current []string
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "# ") && len(current) > 0 {
				blocks = append(blocks, strings.Join(current, "\n"))
				current = nil
			}
			current = append(current, line)
		}
		if len(current) > 0 {
			blocks = append(blocks, strings.Join(current, "\n"))
		}
	}
	slides := make([]officeSlide, 0, len(blocks))
	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 || strings.TrimSpace(block) == "" {
			continue
		}
		slideTitle := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(lines[0]), "#"))
		body := []string{}
		for _, line := range lines[1:] {
			line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*"))
			if line != "" {
				body = append(body, line)
			}
		}
		if slideTitle == "" {
			slideTitle = title
		}
		slides = append(slides, officeSlide{Title: slideTitle, Body: body})
		if len(slides) == 30 {
			break
		}
	}
	if len(slides) == 0 {
		slides = append(slides, officeSlide{Title: title})
	}
	return slides
}

func pptTextParagraph(value string, size int, bullet bool) string {
	properties := ""
	if bullet {
		properties = `<a:pPr lvl="0"><a:buChar char="•"/></a:pPr>`
	}
	return `<a:p>` + properties + `<a:r><a:rPr lang="zh-CN" sz="` + fmt.Sprint(size) + `"/><a:t>` + escapeOfficeXML(value) + `</a:t></a:r><a:endParaRPr lang="zh-CN"/></a:p>`
}

func pptSlideXML(slide officeSlide) string {
	body := strings.Builder{}
	for _, line := range slide.Body {
		body.WriteString(pptTextParagraph(line, 2000, true))
	}
	if len(slide.Body) == 0 {
		body.WriteString(`<a:p><a:endParaRPr lang="zh-CN"/></a:p>`)
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr><p:sp><p:nvSpPr><p:cNvPr id="2" name="Title"/><p:cNvSpPr/><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:lstStyle/>` + pptTextParagraph(slide.Title, 3200, false) + `</p:txBody></p:sp><p:sp><p:nvSpPr><p:cNvPr id="3" name="Content"/><p:cNvSpPr/><p:nvPr><p:ph idx="1"/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:lstStyle/>` + body.String() + `</p:txBody></p:sp></p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`
}

func createPPTX(title, content string) ([]byte, error) {
	slides := parseOfficeSlides(title, content)
	files := map[string]string{
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/></Relationships>`,
		"ppt/presentation.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst><p:sldIdLst>` + func() string {
			var value strings.Builder
			for index := range slides {
				value.WriteString(fmt.Sprintf(`<p:sldId id="%d" r:id="rId%d"/>`, 256+index, index+2))
			}
			return value.String()
		}() + `</p:sldIdLst><p:sldSz cx="12192000" cy="6858000" type="screen16x9"/><p:notesSz cx="6858000" cy="9144000"/></p:presentation>`,
		"ppt/slideMasters/slideMaster1.xml":            `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sldMaster xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr></p:spTree></p:cSld><p:clrMap accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" bg1="lt1" bg2="lt2" folHlink="folHlink" hlink="hlink" tx1="dk1" tx2="dk2"/><p:sldLayoutIdLst><p:sldLayoutId id="1" r:id="rId1"/></p:sldLayoutIdLst><p:txStyles><p:titleStyle/><p:bodyStyle/><p:otherStyle/></p:txStyles></p:sldMaster>`,
		"ppt/slideMasters/_rels/slideMaster1.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/></Relationships>`,
		"ppt/slideLayouts/slideLayout1.xml":            `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sldLayout xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" type="obj"><p:cSld name="Title and Content"><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr></p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sldLayout>`,
		"ppt/slideLayouts/_rels/slideLayout1.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/></Relationships>`,
		"ppt/theme/theme1.xml":                         `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="ErDai"><a:themeElements><a:clrScheme name="ErDai"><a:dk1><a:srgbClr val="172026"/></a:dk1><a:lt1><a:srgbClr val="FFFFFF"/></a:lt1><a:dk2><a:srgbClr val="263238"/></a:dk2><a:lt2><a:srgbClr val="E7EDF0"/></a:lt2><a:accent1><a:srgbClr val="4FAF83"/></a:accent1><a:accent2><a:srgbClr val="E7C16B"/></a:accent2><a:accent3><a:srgbClr val="5C747E"/></a:accent3><a:accent4><a:srgbClr val="9BD8BF"/></a:accent4><a:accent5><a:srgbClr val="89989F"/></a:accent5><a:accent6><a:srgbClr val="C96B73"/></a:accent6><a:hlink><a:srgbClr val="0563C1"/></a:hlink><a:folHlink><a:srgbClr val="954F72"/></a:folHlink></a:clrScheme><a:fontScheme name="ErDai"><a:majorFont><a:latin typeface="Aptos Display"/><a:ea typeface="Microsoft YaHei"/><a:cs typeface=""/></a:majorFont><a:minorFont><a:latin typeface="Aptos"/><a:ea typeface="Microsoft YaHei"/><a:cs typeface=""/></a:minorFont></a:fontScheme><a:fmtScheme name="ErDai"><a:fillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:fillStyleLst><a:lnStyleLst><a:ln w="9525"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln></a:lnStyleLst><a:effectStyleLst><a:effectStyle><a:effectLst/></a:effectStyle></a:effectStyleLst><a:bgFillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:bgFillStyleLst></a:fmtScheme></a:themeElements></a:theme>`,
	}
	var contentTypes strings.Builder
	contentTypes.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/><Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/><Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/><Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>`)
	var presentationRels strings.Builder
	presentationRels.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>`)
	for index, slide := range slides {
		number := index + 1
		files[fmt.Sprintf("ppt/slides/slide%d.xml", number)] = pptSlideXML(slide)
		files[fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", number)] = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/></Relationships>`
		contentTypes.WriteString(fmt.Sprintf(`<Override PartName="/ppt/slides/slide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>`, number))
		presentationRels.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide%d.xml"/>`, number+1, number))
	}
	contentTypes.WriteString(`</Types>`)
	presentationRels.WriteString(`</Relationships>`)
	files["[Content_Types].xml"] = contentTypes.String()
	files["ppt/_rels/presentation.xml.rels"] = presentationRels.String()
	return buildOfficeArchive(files)
}

func safeOfficeFilename(value, fallback, extension string) string {
	value = strings.TrimSpace(filepath.Base(value))
	if value == "." || value == "" {
		value = fallback
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`\\/:*?"<>|`, r) {
			return '-'
		}
		return r
	}, value)
	value = strings.TrimSpace(strings.TrimSuffix(value, filepath.Ext(value)))
	if runes := []rune(value); len(runes) > 80 {
		value = string(runes[:80])
	}
	if value == "" {
		value = "二呆文档"
	}
	return value + extension
}

func (a *AgentRuntime) storeOfficeFile(data []byte, displayName, extension, mimeType string) (agentAttachment, error) {
	if len(data) == 0 || len(data) > maxToolBody || strings.TrimSpace(a.mediaDir) == "" {
		return agentAttachment{}, errors.New("generated document is invalid")
	}
	if err := os.MkdirAll(a.mediaDir, 0o700); err != nil {
		return agentAttachment{}, err
	}
	id, err := randomID("document")
	if err != nil {
		return agentAttachment{}, err
	}
	temporary, err := os.CreateTemp(a.mediaDir, ".document-*.tmp")
	if err != nil {
		return agentAttachment{}, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return agentAttachment{}, err
	}
	name := id + extension
	if err = os.Rename(temporaryName, filepath.Join(a.mediaDir, name)); err != nil {
		return agentAttachment{}, err
	}
	return agentAttachment{Kind: "file", LocalPath: mediaMountRoot + "/" + name, Name: displayName, MimeType: mimeType}, nil
}

func (a *AgentRuntime) createOfficeDocument(ctx context.Context, arguments map[string]any) (toolResult, error) {
	format := strings.ToLower(strings.TrimSpace(stringArgument(arguments, "format")))
	title := strings.TrimSpace(stringArgument(arguments, "title"))
	content := strings.TrimSpace(stringArgument(arguments, "content"))
	filename := strings.TrimSpace(stringArgument(arguments, "filename"))
	if title == "" || content == "" || len([]rune(content)) > maxOfficeContentRunes {
		return toolResult{}, errors.New("document title or content is invalid")
	}
	var data []byte
	var err error
	var extension, mimeType string
	switch format {
	case "docx":
		extension, mimeType = ".docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		data, err = createDOCX(title, content)
	case "pptx":
		extension, mimeType = ".pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation"
		data, err = createPPTX(title, content)
	case "xlsx":
		extension, mimeType = ".xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		data, err = createXLSX(title, content)
	case "md":
		extension, mimeType = ".md", "text/markdown"
		data = []byte("# " + title + "\n\n" + content + "\n")
	case "csv":
		extension, mimeType = ".csv", "text/csv"
		if _, err = spreadsheetRows(content); err == nil {
			data = []byte(content + "\n")
		}
	default:
		return toolResult{}, errors.New("document format is unsupported")
	}
	if err != nil {
		return toolResult{}, err
	}
	displayName := safeOfficeFilename(filename, title, extension)
	attachment, err := a.storeOfficeFile(data, displayName, extension, mimeType)
	if err != nil {
		return toolResult{}, err
	}
	var policy runtimeMessagePolicy
	_ = a.integrationConfig(ctx, "message_policy", &policy)
	encoded, _ := json.Marshal(map[string]any{"ok": true, "format": format, "filename": displayName})
	return toolResult{
		Content: string(encoded), Attachments: []agentAttachment{attachment},
		UserMessage: documentCompletionMessage(policy),
	}, nil
}
