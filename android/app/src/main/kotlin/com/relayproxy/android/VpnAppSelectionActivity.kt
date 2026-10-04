package com.relayproxy.android

import android.app.Activity
import android.content.Intent
import android.content.res.ColorStateList
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.Drawable
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.Editable
import android.text.TextUtils
import android.text.TextWatcher
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.HorizontalScrollView
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast

/**
 * 经过现代体验与性能重构的 VPN 应用选择器页面。
 *
 * 特性：
 * - 顶部返回与标题导航栏，状态栏安全区自适应
 * - 实时应用图标渲染 (40×40dp)，辅以单行省略与高对比度文字
 * - 防抖实时搜索 (100ms Debounce) 与一键清除搜索框 (✕)
 * - 快速筛选胶囊：全部 / 仅看已选 / 全选 / 清空 / 反选
 * - 整行点击响应 + 选中高亮底色背景，操作零延迟
 * - 底部吸底悬浮保存栏，多端屏幕友好
 */
class VpnAppSelectionActivity : Activity() {
    companion object {
        const val EXTRA_SELECTED = "vpnSelectedPackages"
        const val EXTRA_TITLE = "selectionTitle"
        const val EXTRA_SUBTITLE = "selectionSubtitle"
    }

    private data class AppEntry(
        val label: String,
        val packageName: String,
        val icon: Drawable? = null
    )

    private val selected = linkedSetOf<String>()
    private var entries = emptyList<AppEntry>()
    private var currentQuery: String = ""
    private var filterOnlySelected: Boolean = false

    private val debounceHandler = Handler(Looper.getMainLooper())
    private val debounceRunnable = Runnable { renderApps() }

    private lateinit var appList: LinearLayout
    private lateinit var countSubtitle: TextView
    private lateinit var chipAll: TextView
    private lateinit var chipOnlySelected: TextView
    private lateinit var summaryTitle: TextView
    private lateinit var summarySub: TextView
    private lateinit var doneButton: Button
    private lateinit var searchInput: EditText
    private lateinit var clearSearchBtn: TextView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        UiPalette.isDark = ConfigStore(this).isDarkTheme()
        setTheme(if (UiPalette.isDark) R.style.Theme_RelayProxy_Dark else R.style.Theme_RelayProxy_Light)
        configureWindow()

        @Suppress("DEPRECATION")
        selected += (savedInstanceState?.getStringArrayList(EXTRA_SELECTED)
            ?: intent.getStringArrayListExtra(EXTRA_SELECTED)).orEmpty()
            .filter { it != packageName }

        entries = loadApps()
        setContentView(buildUi())
        renderApps()
        updateCounters()
    }

    private fun configureWindow() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            window.setDecorFitsSystemWindows(true)
        }
        window.statusBarColor = UiPalette.bg
        window.navigationBarColor = UiPalette.surface
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            var flags = window.decorView.systemUiVisibility
            flags = if (!UiPalette.isDark) {
                flags or View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR
            } else {
                flags and View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR.inv()
            }
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                flags = if (!UiPalette.isDark) {
                    flags or View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR
                } else {
                    flags and View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR.inv()
                }
            }
            @Suppress("DEPRECATION")
            window.decorView.systemUiVisibility = flags
        }
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putStringArrayList(EXTRA_SELECTED, ArrayList(selected))
    }

    private fun loadApps(): List<AppEntry> {
        val launchIntent = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
        @Suppress("DEPRECATION")
        val launchable = packageManager.queryIntentActivities(launchIntent, 0)
            .mapNotNull { resolved ->
                val pkg = resolved.activityInfo?.packageName?.trim().orEmpty()
                if (pkg.isBlank() || pkg == packageName) return@mapNotNull null
                val label = runCatching { resolved.loadLabel(packageManager).toString() }.getOrNull()?.ifBlank { pkg } ?: pkg
                val icon = runCatching { resolved.loadIcon(packageManager) }.getOrNull()
                AppEntry(label, pkg, icon)
            }
            .distinctBy(AppEntry::packageName)
            .toMutableList()

        val known = launchable.mapTo(hashSetOf(), AppEntry::packageName)
        selected.filterNot { it in known }.forEach { pkg ->
            val appInfo = runCatching { packageManager.getApplicationInfo(pkg, 0) }.getOrNull()
            val label = appInfo?.let { runCatching { packageManager.getApplicationLabel(it).toString() }.getOrNull() } ?: "$pkg（不可见或已卸载）"
            val icon = appInfo?.let { runCatching { packageManager.getApplicationIcon(it) }.getOrNull() }
            launchable += AppEntry(label, pkg, icon)
        }

        return launchable.sortedWith(compareBy(String.CASE_INSENSITIVE_ORDER) { it.label })
    }

    private fun buildUi(): View {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(UiPalette.bg)
            fitsSystemWindows = true
        }

        // 1. 顶部操作导航栏 (返回、标题、副标题统计)
        val topNav = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            val padH = dp(16)
            setPadding(padH, dp(14), padH, dp(8))
        }

        val navBarRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }

        // 现代质感圆角返回按钮卡片 (38×38dp)
        val backBtn = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER
            val sz = dp(38)
            layoutParams = LinearLayout.LayoutParams(sz, sz).apply { rightMargin = dp(12) }
            background = UiKit.rounded(this@VpnAppSelectionActivity, UiPalette.surface, 10, UiPalette.lineSubtle)
            isClickable = true
            isFocusable = true
            val icon = ImageView(this@VpnAppSelectionActivity).apply {
                setImageResource(R.drawable.ic_back)
                imageTintList = ColorStateList.valueOf(UiPalette.ink)
                val iconSz = dp(18)
                layoutParams = LinearLayout.LayoutParams(iconSz, iconSz)
            }
            addView(icon)
            setOnClickListener { finish() }
        }
        navBarRow.addView(backBtn)

        val titleCol = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }

        val titleView = TextView(this).apply {
            text = intent.getStringExtra(EXTRA_TITLE) ?: "选择 VPN 接管应用"
            textSize = 18f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
        }
        titleCol.addView(titleView)

        countSubtitle = TextView(this).apply {
            text = intent.getStringExtra(EXTRA_SUBTITLE) ?: "选择需要由 VPN 代理或排除的已安装应用"
            textSize = 11.5f
            setTextColor(UiPalette.muted)
            setPadding(0, dp(2), 0, 0)
        }
        titleCol.addView(countSubtitle)
        navBarRow.addView(titleCol, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        topNav.addView(navBarRow)
        root.addView(topNav)

        // 2. 搜索框与快捷清除
        val searchContainer = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            val pH = dp(12)
            setPadding(pH, 0, pH, 0)
            background = UiKit.rounded(this@VpnAppSelectionActivity, UiPalette.inputBg, 12, UiPalette.lineSubtle)
            val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(44)).apply {
                leftMargin = dp(16)
                rightMargin = dp(16)
                topMargin = dp(8)
                bottomMargin = dp(8)
            }
            layoutParams = lp
        }

        val searchIcon = TextView(this).apply {
            text = "🔍"
            textSize = 12f
            setPadding(0, 0, dp(6), 0)
        }
        searchContainer.addView(searchIcon)

        searchInput = EditText(this).apply {
            hint = "搜索应用名称或包名…"
            textSize = 13.5f
            setTextColor(UiPalette.ink)
            setHintTextColor(UiPalette.placeholder)
            background = null
            setSingleLine(true)
            addTextChangedListener(object : TextWatcher {
                override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit
                override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {
                    currentQuery = s?.toString().orEmpty()
                    clearSearchBtn.visibility = if (currentQuery.isNotBlank()) View.VISIBLE else View.GONE
                    debounceHandler.removeCallbacks(debounceRunnable)
                    debounceHandler.postDelayed(debounceRunnable, 100)
                }
                override fun afterTextChanged(s: Editable?) = Unit
            })
        }
        searchContainer.addView(searchInput, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.MATCH_PARENT, 1f))

        clearSearchBtn = TextView(this).apply {
            text = "✕"
            textSize = 13f
            setTextColor(UiPalette.muted)
            typeface = Typeface.DEFAULT_BOLD
            visibility = View.GONE
            isClickable = true
            isFocusable = true
            setPadding(dp(8), dp(4), dp(4), dp(4))
            setOnClickListener {
                searchInput.setText("")
            }
        }
        searchContainer.addView(clearSearchBtn)
        root.addView(searchContainer)

        // 3. 筛选与快捷批量操作 Chips
        val chipsScroll = HorizontalScrollView(this).apply {
            isHorizontalScrollBarEnabled = false
            overScrollMode = View.OVER_SCROLL_NEVER
            val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                leftMargin = dp(16)
                rightMargin = dp(16)
                bottomMargin = dp(8)
            }
            layoutParams = lp
        }

        val chipsRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }

        chipAll = buildChip("全部", isActive = true) {
            filterOnlySelected = false
            updateChipStyles()
            renderApps()
        }
        chipsRow.addView(chipAll)

        chipOnlySelected = buildChip("仅看已选", isActive = false) {
            filterOnlySelected = true
            updateChipStyles()
            renderApps()
        }
        chipsRow.addView(chipOnlySelected, marginStart(8))

        // 分割细线
        val divider = View(this).apply {
            setBackgroundColor(UiPalette.line)
            val lp = LinearLayout.LayoutParams(dp(1), dp(14)).apply {
                leftMargin = dp(10)
                rightMargin = dp(10)
            }
            layoutParams = lp
        }
        chipsRow.addView(divider)

        val chipSelectAll = buildActionButton("全选") {
            val visiblePackages = getVisibleEntries().map { it.packageName }
            selected += visiblePackages
            updateCounters()
            renderApps()
            Toast.makeText(this, "已勾选 ${visiblePackages.size} 个应用", Toast.LENGTH_SHORT).show()
        }
        chipsRow.addView(chipSelectAll)

        val chipClear = buildActionButton("清空") {
            selected.clear()
            updateCounters()
            renderApps()
            Toast.makeText(this, "已清空已选应用", Toast.LENGTH_SHORT).show()
        }
        chipsRow.addView(chipClear, marginStart(8))

        val chipInvert = buildActionButton("反选") {
            val visible = getVisibleEntries()
            visible.forEach { entry ->
                if (entry.packageName in selected) selected.remove(entry.packageName)
                else selected.add(entry.packageName)
            }
            updateCounters()
            renderApps()
            Toast.makeText(this, "已反选应用", Toast.LENGTH_SHORT).show()
        }
        chipsRow.addView(chipInvert, marginStart(8))

        chipsScroll.addView(chipsRow)
        root.addView(chipsScroll)

        // 4. 应用列表视窗 (带有舒适内边距)
        appList = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            val p = dp(16)
            setPadding(p, dp(4), p, dp(16))
        }

        val scroll = ScrollView(this).apply {
            isFillViewport = true
            addView(appList)
        }
        root.addView(scroll, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))

        // 5. 底部固定保存确认操作栏
        val bottomBar = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            val pH = dp(18)
            val pV = dp(12)
            setPadding(pH, pV, pH, pV)
            setBackgroundColor(UiPalette.surface)
            background = UiKit.rounded(this@VpnAppSelectionActivity, UiPalette.surface, 0, UiPalette.line, strokeWidthDp = 1)
        }

        val summaryCol = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }
        summaryTitle = TextView(this).apply {
            text = "已选 0 个应用"
            textSize = 14f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
        }
        summaryCol.addView(summaryTitle)

        summarySub = TextView(this).apply {
            text = "未勾选应用将根据分流模式直连"
            textSize = 11f
            setTextColor(UiPalette.muted)
            setPadding(0, dp(2), 0, 0)
        }
        summaryCol.addView(summarySub)

        bottomBar.addView(summaryCol, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))

        doneButton = Button(this).apply {
            text = "保存设置"
            textSize = 14f
            setTextColor(Color.WHITE)
            typeface = Typeface.DEFAULT_BOLD
            background = UiKit.rounded(this@VpnAppSelectionActivity, UiPalette.brand, 10)
            val pH = dp(22)
            setPadding(pH, 0, pH, 0)
            setOnClickListener {
                finishWithResult()
            }
        }
        bottomBar.addView(doneButton, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, dp(44)))
        root.addView(bottomBar)

        return root
    }

    private fun buildChip(text: String, isActive: Boolean, onClick: () -> Unit): TextView {
        return TextView(this).apply {
            this.text = text
            textSize = 12f
            typeface = Typeface.DEFAULT_BOLD
            isClickable = true
            isFocusable = true
            val pH = dp(12)
            val pV = dp(6)
            setPadding(pH, pV, pH, pV)
            styleChip(this, isActive)
            setOnClickListener { onClick() }
        }
    }

    private fun styleChip(view: TextView, isActive: Boolean) {
        if (isActive) {
            view.setTextColor(UiPalette.brand)
            view.background = UiKit.rounded(this, UiPalette.brandSoft, 14, UiPalette.brandSoftBorder)
        } else {
            view.setTextColor(UiPalette.muted)
            view.background = UiKit.rounded(this, UiPalette.surfaceElevated, 14, UiPalette.lineSubtle)
        }
    }

    private fun updateChipStyles() {
        styleChip(chipAll, !filterOnlySelected)
        styleChip(chipOnlySelected, filterOnlySelected)
    }

    private fun buildActionButton(text: String, onClick: () -> Unit): TextView {
        return TextView(this).apply {
            this.text = text
            textSize = 12f
            setTextColor(UiPalette.ink)
            typeface = Typeface.DEFAULT_BOLD
            isClickable = true
            isFocusable = true
            val pH = dp(11)
            val pV = dp(6)
            setPadding(pH, pV, pH, pV)
            background = UiKit.rounded(this@VpnAppSelectionActivity, UiPalette.surface, 14, UiPalette.lineSubtle)
            setOnClickListener { onClick() }
        }
    }

    private fun getVisibleEntries(): List<AppEntry> {
        val query = currentQuery.trim()
        return entries.filter { entry ->
            val matchesQuery = query.isBlank() ||
                entry.label.contains(query, ignoreCase = true) ||
                entry.packageName.contains(query, ignoreCase = true)
            val matchesFilter = !filterOnlySelected || entry.packageName in selected
            matchesQuery && matchesFilter
        }
    }

    private fun renderApps() {
        if (!::appList.isInitialized) return
        appList.removeAllViews()

        val visible = getVisibleEntries()

        if (visible.isEmpty()) {
            val emptyCard = UiKit.card(this, paddingDp = 28, radiusDp = 14)
            val col = LinearLayout(this).apply {
                orientation = LinearLayout.VERTICAL
                gravity = Gravity.CENTER
            }
            col.addView(TextView(this).apply {
                text = if (filterOnlySelected) "📱" else "🔍"
                textSize = 32f
                gravity = Gravity.CENTER
            })
            col.addView(TextView(this).apply {
                text = if (filterOnlySelected) "暂未勾选任何应用" else "未找到匹配的应用"
                textSize = 14.5f
                setTextColor(UiPalette.ink)
                typeface = Typeface.DEFAULT_BOLD
                gravity = Gravity.CENTER
                setPadding(0, dp(10), 0, 0)
            })
            col.addView(TextView(this).apply {
                text = if (filterOnlySelected) "点击上方「全部」列表勾选需要接管的应用" else "尝试输入其他应用名称或包名重新搜索"
                textSize = 12f
                setTextColor(UiPalette.muted)
                gravity = Gravity.CENTER
                setPadding(0, dp(4), 0, 0)
            })
            emptyCard.addView(col)
            val lp = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                topMargin = dp(16)
            }
            appList.addView(emptyCard, lp)
            return
        }

        visible.forEach { entry ->
            val isChecked = entry.packageName in selected

            val row = LinearLayout(this).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER_VERTICAL
                val pH = dp(12)
                val pV = dp(10)
                setPadding(pH, pV, pH, pV)
                background = UiKit.rounded(
                    this@VpnAppSelectionActivity,
                    if (isChecked) UiPalette.brandSoft else UiPalette.surface,
                    12,
                    if (isChecked) UiPalette.brandSoftBorder else UiPalette.lineSubtle
                )
                isClickable = true
                isFocusable = true
            }

            // 应用图标 (40×40dp)
            val iconView = ImageView(this).apply {
                if (entry.icon != null) {
                    setImageDrawable(entry.icon)
                } else {
                    setImageResource(android.R.drawable.sym_def_app_icon)
                }
                val sz = dp(40)
                layoutParams = LinearLayout.LayoutParams(sz, sz)
            }
            row.addView(iconView)

            // 应用名称与包名
            val textCol = LinearLayout(this).apply {
                orientation = LinearLayout.VERTICAL
                val lp = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                    leftMargin = dp(12)
                    rightMargin = dp(8)
                }
                layoutParams = lp
            }

            textCol.addView(TextView(this).apply {
                text = entry.label
                textSize = 14f
                setTextColor(UiPalette.ink)
                typeface = Typeface.DEFAULT_BOLD
                maxLines = 1
                ellipsize = TextUtils.TruncateAt.END
            })

            textCol.addView(TextView(this).apply {
                text = entry.packageName
                textSize = 11.5f
                setTextColor(UiPalette.muted)
                maxLines = 1
                ellipsize = TextUtils.TruncateAt.END
                setPadding(0, dp(2), 0, 0)
            })
            row.addView(textCol)

            // 选择框
            val checkBox = CheckBox(this).apply {
                this.isChecked = isChecked
                isClickable = false
                isFocusable = false
                buttonTintList = ColorStateList(
                    arrayOf(intArrayOf(android.R.attr.state_checked), intArrayOf()),
                    intArrayOf(UiPalette.brand, UiPalette.muted)
                )
            }
            row.addView(checkBox)

            // 行点击事件 (即时局部刷新，零卡顿)
            row.setOnClickListener {
                val nowChecked = entry.packageName !in selected
                if (nowChecked) selected += entry.packageName else selected -= entry.packageName
                checkBox.isChecked = nowChecked
                row.background = UiKit.rounded(
                    this@VpnAppSelectionActivity,
                    if (nowChecked) UiPalette.brandSoft else UiPalette.surface,
                    12,
                    if (nowChecked) UiPalette.brandSoftBorder else UiPalette.lineSubtle
                )
                updateCounters()
                if (filterOnlySelected && !nowChecked) {
                    row.visibility = View.GONE
                }
            }

            val lpRow = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                topMargin = dp(6)
            }
            appList.addView(row, lpRow)
        }
    }

    private fun updateCounters() {
        if (!::countSubtitle.isInitialized) return
        val count = selected.size
        val total = entries.size
        countSubtitle.text = "共 $total 个应用 · 已选 $count 个"
        chipAll.text = "全部 ($total)"
        chipOnlySelected.text = "已选 ($count)"
        summaryTitle.text = "已勾选 $count 个应用"
        summarySub.text = if (count > 0) "已选应用将在生效后由 VPN 代理/排除" else "未勾选应用，全部直接走原链路"
        doneButton.text = "保存设置 ($count)"
    }

    private fun finishWithResult() {
        setResult(
            RESULT_OK,
            Intent().putStringArrayListExtra(EXTRA_SELECTED, ArrayList(selected.sorted())),
        )
        finish()
    }

    private fun marginStart(dpValue: Int) = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.WRAP_CONTENT,
        ViewGroup.LayoutParams.WRAP_CONTENT
    ).apply { leftMargin = dp(dpValue) }

    private fun dp(value: Int) = UiKit.dp(this, value)
}
